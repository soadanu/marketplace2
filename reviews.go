package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sellerRating returns the average star rating and number of reviews.
func (a *App) sellerRating(sellerID string) (float64, int) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	sum, n := 0, 0
	for _, rv := range a.store.Reviews {
		if rv.SellerID == sellerID {
			sum += rv.Rating
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return float64(sum) / float64(n), n
}

// handleReviewCreate saves a buyer's rating of a seller. Only the buyer of
// a delivered order can review, and only once per order, so ratings can't
// be inflated by people who never bought anything.
func (a *App) handleReviewCreate(w http.ResponseWriter, r *http.Request, buyer *User) {
	orderID := r.FormValue("order_id")
	rating, err := strconv.Atoi(r.FormValue("rating"))
	comment := strings.TrimSpace(r.FormValue("comment"))
	if len(comment) > 500 {
		comment = comment[:500]
	}

	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	order, ok := a.store.Orders[orderID]
	if !ok || order.BuyerID != buyer.ID || order.Status != "delivered" || err != nil || rating < 1 || rating > 5 {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}
	for _, rv := range a.store.Reviews {
		if rv.OrderID == orderID {
			http.Redirect(w, r, "/orders", http.StatusSeeOther)
			return
		}
	}
	rv := &Review{ID: newID(), OrderID: orderID, BuyerID: buyer.ID, SellerID: order.SellerID, Rating: rating, Comment: comment, CreatedAt: time.Now()}
	a.store.Reviews[rv.ID] = rv
	a.store.save()
	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

type reviewView struct {
	Rating  int
	Comment string
	Name    string
	Date    time.Time
}

func firstName(full string) string {
	f := strings.Fields(full)
	if len(f) == 0 {
		return "A buyer"
	}
	return f[0]
}

// handleShop is a seller's public page: their rating, reviews and listings.
func (a *App) handleShop(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	a.store.mu.RLock()
	seller, ok := a.store.Users[id]
	if !ok || seller.Suspended || seller.SellerStatus != "approved" {
		a.store.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
	name, since := seller.Name, seller.CreatedAt
	listings := make([]*Listing, 0)
	for _, l := range a.store.Listings {
		if l.SellerID == id && l.Status == "active" {
			listings = append(listings, l)
		}
	}
	reviews := make([]reviewView, 0)
	for _, rv := range a.store.Reviews {
		if rv.SellerID != id {
			continue
		}
		who := "A buyer"
		if b, ok := a.store.Users[rv.BuyerID]; ok {
			who = firstName(b.Name)
		}
		reviews = append(reviews, reviewView{Rating: rv.Rating, Comment: rv.Comment, Name: who, Date: rv.CreatedAt})
	}
	a.store.mu.RUnlock()

	sort.Slice(listings, func(i, j int) bool { return listings[i].CreatedAt.After(listings[j].CreatedAt) })
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].Date.After(reviews[j].Date) })
	avg, count := a.sellerRating(id)

	render(w, "shop.html", map[string]any{
		"SellerID": id, "SellerName": name, "Since": since,
		"Listings": listings, "Reviews": reviews, "Avg": avg, "Count": count,
		"User": a.currentUser(r),
	})
}
