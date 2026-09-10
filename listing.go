package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// handleHome shows all active listings, with optional category filter (?category=) and search (?q=).
func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	catFilter := r.URL.Query().Get("category")
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	a.store.mu.RLock()
	listings := make([]*Listing, 0)
	for _, l := range a.store.Listings {
		if l.Status != "active" {
			continue
		}
		if catFilter != "" && l.CategoryID != catFilter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Title), q) && !strings.Contains(strings.ToLower(l.Description), q) {
			continue
		}
		listings = append(listings, l)
	}
	a.store.mu.RUnlock()

	sort.Slice(listings, func(i, j int) bool { return listings[i].CreatedAt.After(listings[j].CreatedAt) })

	render(w, "home.html", map[string]any{
		"Listings":   listings,
		"Categories": a.sortedCategories(),
		"Query":      q,
		"CategoryID": catFilter,
		"User":       a.currentUser(r),
	})
}

func (a *App) handleListingDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	a.store.mu.RLock()
	listing, ok := a.store.Listings[id]
	var seller *User
	if ok {
		seller = a.store.Users[listing.SellerID]
	}
	a.store.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}
	render(w, "listing_detail.html", map[string]any{
		"Listing":         listing,
		"Seller":          seller,
		"User":            a.currentUser(r),
		"SellerCanBePaid": seller != nil && (!a.payments.Configured() || seller.FlutterwaveSubaccountID != ""),
	})
}

// --- Seller-side listing management ---

func (a *App) handleSellerListingsGet(w http.ResponseWriter, r *http.Request, seller *User) {
	a.store.mu.RLock()
	mine := make([]*Listing, 0)
	for _, l := range a.store.Listings {
		if l.SellerID == seller.ID {
			mine = append(mine, l)
		}
	}
	a.store.mu.RUnlock()
	sort.Slice(mine, func(i, j int) bool { return mine[i].CreatedAt.After(mine[j].CreatedAt) })

	render(w, "seller_listings.html", map[string]any{
		"Listings":   mine,
		"Categories": a.sortedCategories(),
		"User":       seller,
	})
}

const maxListingImages = 6

func (a *App) handleSellerListingCreate(w http.ResponseWriter, r *http.Request, seller *User) {
	if err := r.ParseMultipartForm(25 << 20); err != nil { // 25MB limit, enough for several images
		http.Error(w, "form too large or malformed", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	desc := strings.TrimSpace(r.FormValue("description"))
	categoryID := r.FormValue("category_id")
	priceNaira, err1 := strconv.ParseFloat(r.FormValue("price"), 64)
	stock, err2 := strconv.Atoi(r.FormValue("stock"))

	if title == "" || categoryID == "" || err1 != nil || priceNaira <= 0 || err2 != nil || stock < 0 {
		http.Error(w, "invalid listing details", http.StatusBadRequest)
		return
	}

	imagePaths, err := saveUploadedFiles(r, "images", dataPath("uploads", "listings"), maxListingImages)
	if err != nil {
		http.Error(w, "could not save images: "+err.Error(), http.StatusBadRequest)
		return
	}

	listing := &Listing{
		ID:          newID(),
		SellerID:    seller.ID,
		CategoryID:  categoryID,
		Title:       title,
		Description: desc,
		PriceKobo:   int(priceNaira * 100),
		Stock:       stock,
		ImagePaths:  imagePaths,
		Status:      "active",
		CreatedAt:   time.Now(),
	}

	a.store.mu.Lock()
	a.store.Listings[listing.ID] = listing
	a.store.save()
	a.store.mu.Unlock()

	http.Redirect(w, r, "/seller/listings", http.StatusSeeOther)
}

func (a *App) handleSellerListingRemove(w http.ResponseWriter, r *http.Request, seller *User) {
	id := r.FormValue("id")
	a.store.mu.Lock()
	if l, ok := a.store.Listings[id]; ok && l.SellerID == seller.ID {
		l.Status = "removed"
		a.store.save()
	}
	a.store.mu.Unlock()
	http.Redirect(w, r, "/seller/listings", http.StatusSeeOther)
}

// saveUploadedFiles reads a multipart file field that may contain several
// files (an <input multiple>), content-sniffs each one, and writes them
// under dir with random names. Silently skips anything past maxCount rather
// than erroring, so a seller can't be blocked by picking one extra photo.
// Returns an empty slice (not an error) if the field was empty - a listing
// without photos is allowed.
func saveUploadedFiles(r *http.Request, field, dir string, maxCount int) ([]string, error) {
	if r.MultipartForm == nil || r.MultipartForm.File == nil {
		return nil, nil
	}
	headers := r.MultipartForm.File[field]
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > maxCount {
		headers = headers[:maxCount]
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(headers))
	for _, header := range headers {
		file, err := header.Open()
		if err != nil {
			return nil, err
		}

		buf := make([]byte, 512)
		n, _ := file.Read(buf)
		contentType := http.DetectContentType(buf[:n])
		if !strings.HasPrefix(contentType, "image/") {
			file.Close()
			return nil, fmt.Errorf("%s is not an image (detected %s)", header.Filename, contentType)
		}
		file.Seek(0, io.SeekStart)

		ext := filepath.Ext(header.Filename)
		name := newID() + ext
		fullPath := filepath.Join(dir, name)

		out, err := os.Create(fullPath)
		if err != nil {
			file.Close()
			return nil, err
		}
		_, copyErr := io.Copy(out, file)
		out.Close()
		file.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		paths = append(paths, fullPath)
	}
	return paths, nil
}

// saveUploadedFile reads a single multipart file field, content-sniffs it,
// and writes it under dir with a random name. Returns "" if the field was
// empty. Still used for single-file uploads like the KYC document.
func saveUploadedFile(r *http.Request, field, dir string) (string, error) {
	file, header, err := r.FormFile(field)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	contentType := http.DetectContentType(buf[:n])
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("uploaded file is not an image (detected %s)", contentType)
	}
	file.Seek(0, io.SeekStart)

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	ext := filepath.Ext(header.Filename)
	name := newID() + ext
	fullPath := filepath.Join(dir, name)

	out, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		return "", err
	}
	return fullPath, nil
}
