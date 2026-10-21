package main

import (
	"net/http"
	"sort"
	"time"
)

func favKey(userID, listingID string) string { return userID + "|" + listingID }

func (a *App) isFavorite(userID, listingID string) bool {
	if userID == "" {
		return false
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	_, ok := a.store.Favorites[favKey(userID, listingID)]
	return ok
}

// handleFavoriteToggle saves or un-saves a listing for the logged-in user.
func (a *App) handleFavoriteToggle(w http.ResponseWriter, r *http.Request, user *User) {
	id := r.FormValue("listing_id")
	a.store.mu.Lock()
	if l, ok := a.store.Listings[id]; ok && l.Status != "removed" {
		k := favKey(user.ID, id)
		if _, saved := a.store.Favorites[k]; saved {
			delete(a.store.Favorites, k)
		} else {
			a.store.Favorites[k] = &Favorite{UserID: user.ID, ListingID: id, CreatedAt: time.Now()}
		}
		a.store.save()
	}
	a.store.mu.Unlock()
	http.Redirect(w, r, "/listing?id="+id, http.StatusSeeOther)
}

func (a *App) handleFavorites(w http.ResponseWriter, r *http.Request, user *User) {
	a.store.mu.RLock()
	type saved struct {
		l  *Listing
		at time.Time
	}
	found := make([]saved, 0)
	for _, f := range a.store.Favorites {
		if f.UserID != user.ID {
			continue
		}
		if l, ok := a.store.Listings[f.ListingID]; ok && l.Status == "active" {
			if s, ok := a.store.Users[l.SellerID]; ok && !s.Suspended {
				found = append(found, saved{l, f.CreatedAt})
			}
		}
	}
	a.store.mu.RUnlock()

	sort.Slice(found, func(i, j int) bool { return found[i].at.After(found[j].at) })
	listings := make([]*Listing, 0, len(found))
	for _, s := range found {
		listings = append(listings, s.l)
	}
	render(w, "favorites.html", map[string]any{"Listings": listings, "User": user})
}
