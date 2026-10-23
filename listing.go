package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const homePageSize = 12

// parseNairaParam reads a price filter typed in naira and returns kobo, or
// -1 when it is empty or not a sensible amount.
func parseNairaParam(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || f < 0 || f > 100000000 {
		return -1
	}
	return int(math.Round(f * 100))
}

// handleHome shows active listings with search, category, price range,
// sorting and paging (?q= &category= &min= &max= &sort= &page=).
func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	qp := r.URL.Query()
	catFilter := qp.Get("category")
	rawQ := strings.TrimSpace(qp.Get("q"))
	q := strings.ToLower(rawQ)
	sortBy := qp.Get("sort")
	if sortBy != "price_asc" && sortBy != "price_desc" {
		sortBy = "newest"
	}
	minKobo, maxKobo := parseNairaParam(qp.Get("min")), parseNairaParam(qp.Get("max"))
	page, _ := strconv.Atoi(qp.Get("page"))
	if page < 1 {
		page = 1
	}

	a.store.mu.RLock()
	listings := make([]*Listing, 0)
	for _, l := range a.store.Listings {
		if l.Status != "active" {
			continue
		}
		if s, ok := a.store.Users[l.SellerID]; !ok || s.Suspended {
			continue
		}
		if catFilter != "" && l.CategoryID != catFilter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Title), q) && !strings.Contains(strings.ToLower(l.Description), q) {
			continue
		}
		if (minKobo >= 0 && l.PriceKobo < minKobo) || (maxKobo >= 0 && l.PriceKobo > maxKobo) {
			continue
		}
		listings = append(listings, l)
	}
	a.store.mu.RUnlock()

	sort.Slice(listings, func(i, j int) bool {
		if listings[i].PriceKobo != listings[j].PriceKobo {
			switch sortBy {
			case "price_asc":
				return listings[i].PriceKobo < listings[j].PriceKobo
			case "price_desc":
				return listings[i].PriceKobo > listings[j].PriceKobo
			}
		}
		return listings[i].CreatedAt.After(listings[j].CreatedAt)
	})

	total := len(listings)
	totalPages := (total + homePageSize - 1) / homePageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * homePageSize
	end := start + homePageSize
	if end > total {
		end = total
	}

	// Links for the pager keep every active filter.
	base := url.Values{}
	if rawQ != "" {
		base.Set("q", rawQ)
	}
	if catFilter != "" {
		base.Set("category", catFilter)
	}
	if sortBy != "newest" {
		base.Set("sort", sortBy)
	}
	if minKobo >= 0 {
		base.Set("min", qp.Get("min"))
	}
	if maxKobo >= 0 {
		base.Set("max", qp.Get("max"))
	}
	pageURL := func(p int) string {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		if p > 1 {
			v.Set("page", strconv.Itoa(p))
		}
		if enc := v.Encode(); enc != "" {
			return "/?" + enc
		}
		return "/"
	}
	prev, next := "", ""
	if page > 1 {
		prev = pageURL(page - 1)
	}
	if page < totalPages {
		next = pageURL(page + 1)
	}

	render(w, "home.html", map[string]any{
		"Listings":   listings[start:end],
		"Total":      total,
		"Page":       page,
		"TotalPages": totalPages,
		"PrevURL":    prev,
		"NextURL":    next,
		"Sort":       sortBy,
		"Min":        qp.Get("min"),
		"Max":        qp.Get("max"),
		"Categories": a.sortedCategories(),
		"Query":      rawQ,
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

	// Removed listings and suspended sellers' listings are gone for everyone.
	if !ok || listing.Status == "removed" || seller == nil || seller.Suspended {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	uid := ""
	if user != nil {
		uid = user.ID
	}
	avg, count := a.sellerRating(seller.ID)
	render(w, "listing_detail.html", map[string]any{
		"Listing":         listing,
		"Seller":          seller,
		"User":            user,
		"SellerCanBePaid": !a.payments.Configured() || seller.FlutterwaveSubaccountID != "",
		"Avg":             avg,
		"RatingCount":     count,
		"IsFavorite":      a.isFavorite(uid, listing.ID),
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
	f, msg := a.parseListingForm(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	imagePaths, err := saveUploadedFiles(r, "images", dataPath("uploads", "listings"), maxListingImages)
	if err != nil {
		http.Error(w, "could not save images: "+err.Error(), http.StatusBadRequest)
		return
	}

	status := "active"
	if f.Stock == 0 {
		status = "sold_out"
	}
	listing := &Listing{
		ID:          newID(),
		SellerID:    seller.ID,
		CategoryID:  f.CategoryID,
		Title:       f.Title,
		Description: f.Description,
		PriceKobo:   f.PriceKobo,
		Stock:       f.Stock,
		ImagePaths:  imagePaths,
		Status:      status,
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
		ext, isImage := imageExt(contentType)
		if !isImage {
			file.Close()
			return nil, fmt.Errorf("%s is not a JPG, PNG, GIF or WebP image", header.Filename)
		}
		file.Seek(0, io.SeekStart)

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
	file, _, err := r.FormFile(field)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	contentType := http.DetectContentType(buf[:n])
	ext, isImage := imageExt(contentType)
	if !isImage {
		return "", fmt.Errorf("the file must be a JPG, PNG, GIF or WebP image")
	}
	file.Seek(0, io.SeekStart)

	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
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

// imageExt maps a sniffed content type to the file extension we store it
// under. Only these four are accepted.
func imageExt(contentType string) (string, bool) {
	switch contentType {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	}
	return "", false
}

// listingCover is the file name of a listing's first photo, or "" if it has none.
func listingCover(l *Listing) string {
	if len(l.ImagePaths) == 0 {
		return ""
	}
	return filepath.Base(l.ImagePaths[0])
}
