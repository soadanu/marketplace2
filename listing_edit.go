package main

import (
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type listingForm struct {
	Title, Description, CategoryID string
	PriceKobo, Stock               int
}

// parseListingForm validates the fields shared by "create" and "edit".
func (a *App) parseListingForm(r *http.Request) (listingForm, string) {
	f := listingForm{
		Title:       strings.TrimSpace(r.FormValue("title")),
		Description: strings.TrimSpace(r.FormValue("description")),
		CategoryID:  r.FormValue("category_id"),
	}
	price, err1 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price")), 64)
	stock, err2 := strconv.Atoi(strings.TrimSpace(r.FormValue("stock")))

	a.store.mu.RLock()
	_, catOK := a.store.Categories[f.CategoryID]
	a.store.mu.RUnlock()

	switch {
	case f.Title == "" || len(f.Title) > 120:
		return f, "Give the listing a title (up to 120 characters)."
	case len(f.Description) > 2000:
		return f, "The description is too long (2000 characters at most)."
	case err1 != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > 100000000:
		return f, "Enter a price between ₦0.01 and ₦100,000,000."
	case err2 != nil || stock < 0 || stock > 100000:
		return f, "Enter a stock quantity between 0 and 100,000."
	case !catOK:
		return f, "Choose a category."
	}
	// Round, don't truncate: 19.99*100 is 1998.9999... in floating point.
	f.PriceKobo = int(math.Round(price * 100))
	f.Stock = stock
	return f, ""
}

// ownListing returns the listing only if it belongs to seller and hasn't
// been removed.
func (a *App) ownListing(id string, seller *User) *Listing {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	l, ok := a.store.Listings[id]
	if !ok || l.SellerID != seller.ID || l.Status == "removed" {
		return nil
	}
	return l
}

func (a *App) renderListingEdit(w http.ResponseWriter, seller *User, l *Listing, msg string) {
	a.store.mu.RLock()
	names := make([]string, 0, len(l.ImagePaths))
	for _, p := range l.ImagePaths {
		names = append(names, filepath.Base(p))
	}
	a.store.mu.RUnlock()
	render(w, "seller_listing_edit.html", map[string]any{
		"User":       seller,
		"Listing":    l,
		"Images":     names,
		"Categories": a.sortedCategories(),
		"MaxImages":  maxListingImages,
		"Error":      msg,
	})
}

func (a *App) handleSellerListingEditGet(w http.ResponseWriter, r *http.Request, seller *User) {
	l := a.ownListing(r.URL.Query().Get("id"), seller)
	if l == nil {
		http.Redirect(w, r, "/seller/listings", http.StatusSeeOther)
		return
	}
	a.renderListingEdit(w, seller, l, "")
}

func (a *App) handleSellerListingEditPost(w http.ResponseWriter, r *http.Request, seller *User) {
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "form too large or malformed", http.StatusBadRequest)
		return
	}
	l := a.ownListing(r.FormValue("id"), seller)
	if l == nil {
		http.Redirect(w, r, "/seller/listings", http.StatusSeeOther)
		return
	}
	f, msg := a.parseListingForm(r)
	if msg != "" {
		a.renderListingEdit(w, seller, l, msg)
		return
	}

	remove := map[string]bool{}
	for _, v := range r.Form["remove_image"] {
		remove[filepath.Base(v)] = true
	}
	a.store.mu.RLock()
	current := append([]string(nil), l.ImagePaths...)
	a.store.mu.RUnlock()

	var kept, removed []string
	for _, p := range current {
		if remove[filepath.Base(p)] {
			removed = append(removed, p)
		} else {
			kept = append(kept, p)
		}
	}

	var added []string
	if room := maxListingImages - len(kept); room > 0 {
		var err error
		added, err = saveUploadedFiles(r, "images", dataPath("uploads", "listings"), room)
		if err != nil {
			a.renderListingEdit(w, seller, l, "Could not save the photos: "+err.Error())
			return
		}
	}

	a.store.mu.Lock()
	l.Title, l.Description, l.CategoryID = f.Title, f.Description, f.CategoryID
	l.PriceKobo, l.Stock = f.PriceKobo, f.Stock
	l.ImagePaths = append(kept, added...)
	// Restocking puts a sold-out item back on the market, and zero stock
	// takes it off - the same rule the payment code uses.
	if l.Stock > 0 && l.Status == "sold_out" {
		l.Status = "active"
	} else if l.Stock == 0 && l.Status == "active" {
		l.Status = "sold_out"
	}
	a.store.save()
	a.store.mu.Unlock()

	for _, p := range removed {
		os.Remove(filepath.Join(dataPath("uploads", "listings"), filepath.Base(p)))
	}
	http.Redirect(w, r, "/seller/listings", http.StatusSeeOther)
}
