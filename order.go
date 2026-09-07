package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const cartCookieName = "kaya_cart"

// cart is listingID -> quantity, stored client-side in a cookie (JSON,
// URL-encoded). Simple and fine for a v1 - move to server-side per-user
// storage if you want carts to survive across devices.
type cart map[string]int

func readCart(r *http.Request) cart {
	c := cart{}
	cookie, err := r.Cookie(cartCookieName)
	if err != nil {
		return c
	}
	raw, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return c
	}
	json.Unmarshal([]byte(raw), &c)
	return c
}

func writeCart(w http.ResponseWriter, c cart) {
	b, _ := json.Marshal(c)
	http.SetCookie(w, &http.Cookie{
		Name:     cartCookieName,
		Value:    url.QueryEscape(string(b)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
	})
}

func (a *App) handleCartAdd(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("listing_id")
	qty, err := strconv.Atoi(r.FormValue("qty"))
	if err != nil || qty < 1 {
		qty = 1
	}
	c := readCart(r)
	c[id] += qty
	writeCart(w, c)
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

func (a *App) handleCartRemove(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("listing_id")
	c := readCart(r)
	delete(c, id)
	writeCart(w, c)
	http.Redirect(w, r, "/cart", http.StatusSeeOther)
}

type cartLine struct {
	Listing *Listing
	Qty     int
	Subtotal int
}

func (a *App) handleCartView(w http.ResponseWriter, r *http.Request) {
	c := readCart(r)

	a.store.mu.RLock()
	lines := make([]cartLine, 0, len(c))
	total := 0
	for id, qty := range c {
		l, ok := a.store.Listings[id]
		if !ok || l.Status != "active" {
			continue
		}
		sub := l.PriceKobo * qty
		lines = append(lines, cartLine{Listing: l, Qty: qty, Subtotal: sub})
		total += sub
	}
	a.store.mu.RUnlock()

	sort.Slice(lines, func(i, j int) bool { return lines[i].Listing.Title < lines[j].Listing.Title })

	render(w, "cart.html", map[string]any{
		"Lines": lines,
		"Total": total,
		"User":  a.currentUser(r),
	})
}

// handleCheckout creates the Order from the cart. In a real deployment this
// is where you'd redirect to Flutterwave's payment page; here it marks the
// order "placed" and stops - wire in your existing Flutterwave integration
// (initiate here, confirm via webhook, see note below) to take real payment.
func (a *App) handleCheckout(w http.ResponseWriter, r *http.Request, buyer *User) {
	c := readCart(r)
	if len(c) == 0 {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}

	a.store.mu.Lock()
	items := make([]OrderItem, 0, len(c))
	total := 0
	for id, qty := range c {
		l, ok := a.store.Listings[id]
		if !ok || l.Status != "active" || l.Stock < qty {
			continue
		}
		items = append(items, OrderItem{
			ListingID: l.ID,
			SellerID:  l.SellerID,
			Title:     l.Title,
			Quantity:  qty,
			PriceKobo: l.PriceKobo,
		})
		total += l.PriceKobo * qty
		l.Stock -= qty
		if l.Stock == 0 {
			l.Status = "sold_out"
		}
	}

	if len(items) == 0 {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}

	order := &Order{
		ID:        newID(),
		BuyerID:   buyer.ID,
		TotalKobo: total,
		Status:    "placed", // becomes "paid" once the Flutterwave webhook confirms payment
		Items:     items,
		CreatedAt: time.Now(),
	}
	a.store.Orders[order.ID] = order
	a.store.save()
	a.store.mu.Unlock()

	writeCart(w, cart{}) // empty the cart
	http.Redirect(w, r, "/orders", http.StatusSeeOther)

	// NOTE ON PAYMENT: don't trust a redirect alone to mark an order "paid".
	// Initiate the Flutterwave transaction here (pass order.ID as tx_ref),
	// then verify it server-side in a webhook handler before flipping
	// order.Status to "paid". Otherwise a user can forge a success redirect
	// without actually paying.
}

func (a *App) handleOrdersGet(w http.ResponseWriter, r *http.Request, buyer *User) {
	a.store.mu.RLock()
	mine := make([]*Order, 0)
	for _, o := range a.store.Orders {
		if o.BuyerID == buyer.ID {
			mine = append(mine, o)
		}
	}
	a.store.mu.RUnlock()
	sort.Slice(mine, func(i, j int) bool { return mine[i].CreatedAt.After(mine[j].CreatedAt) })

	render(w, "orders.html", map[string]any{"Orders": mine, "User": buyer})
}
