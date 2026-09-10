package main

import (
	"encoding/json"
	"log"
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

// handleBuyNow is the direct "buy this one item" path from a product page -
// skips the cart entirely and takes the buyer straight to payment. Shares
// all the same safety checks as handleCheckout (stock availability check,
// seller payout gating) since it's really just a single-item checkout.
//
// Stock is NOT reserved here - the listing stays visible and purchasable by
// other buyers until this order is actually confirmed paid (see
// applyVerifiedTransaction in payment.go, the only place stock changes).
func (a *App) handleBuyNow(w http.ResponseWriter, r *http.Request, buyer *User) {
	listingID := r.FormValue("listing_id")
	qty, err := strconv.Atoi(r.FormValue("qty"))
	if err != nil || qty < 1 {
		qty = 1
	}

	a.store.mu.Lock()
	l, ok := a.store.Listings[listingID]
	if !ok || l.Status != "active" || l.Stock < qty {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/listing?id="+listingID, http.StatusSeeOther)
		return
	}
	seller, ok := a.store.Users[l.SellerID]
	if !ok || (a.payments.Configured() && seller.FlutterwaveSubaccountID == "") {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/listing?id="+listingID, http.StatusSeeOther)
		return
	}

	total := l.PriceKobo * qty
	fee := int(float64(total) * platformCommissionRate())
	order := &Order{
		ID:               newID(),
		CheckoutGroupID:  newID(),
		BuyerID:          buyer.ID,
		SellerID:         l.SellerID,
		TotalKobo:        total,
		PlatformFeeKobo:  fee,
		SellerPayoutKobo: total - fee,
		Status:           "pending_payment",
		Items: []OrderItem{{
			ListingID: l.ID,
			SellerID:  l.SellerID,
			Title:     l.Title,
			Quantity:  qty,
			PriceKobo: l.PriceKobo,
		}},
		CreatedAt: time.Now(),
	}
	a.store.Orders[order.ID] = order
	a.store.save()
	a.store.mu.Unlock()

	if !a.payments.Configured() {
		a.finalizeOrderPaid(order, "dev-mode")
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}

	a.startOrderPayment(w, r, buyer, order)
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
	Listing  *Listing
	Qty      int
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
		"Lines":       lines,
		"Total":       total,
		"User":        a.currentUser(r),
		"Unavailable": r.URL.Query().Get("unavailable"),
	})
}

// handleCheckout creates one Order per seller represented in the cart,
// tagging them with a shared CheckoutGroupID, then sends the buyer to pay
// the first one. Splitting by seller like this - rather than one combined
// transaction - is what lets each order carry a clean, unambiguous split to
// exactly one seller's Flutterwave subaccount. Each order stays
// "pending_payment" until confirmOrderPaid verifies the transaction with
// Flutterwave directly - never trust a redirect alone.
//
// Stock is NOT reserved at this point - see applyVerifiedTransaction in
// payment.go, the only place stock actually changes, for why.
func (a *App) handleCheckout(w http.ResponseWriter, r *http.Request, buyer *User) {
	c := readCart(r)
	if len(c) == 0 {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}
	commission := platformCommissionRate()

	a.store.mu.Lock()
	itemsBySeller := map[string][]OrderItem{}
	skipped := 0
	remainingCart := cart{}
	for id, qty := range c {
		l, ok := a.store.Listings[id]
		if !ok || l.Status != "active" || l.Stock < qty {
			skipped++
			continue
		}
		seller, ok := a.store.Users[l.SellerID]
		if !ok {
			skipped++
			continue
		}
		// In live mode, a seller with no payout account configured can't be
		// paid safely - skip their items rather than take money we can't
		// route to them.
		if a.payments.Configured() && seller.FlutterwaveSubaccountID == "" {
			skipped++
			remainingCart[id] = qty // leave it in the cart so the buyer isn't just told "gone"
			continue
		}

		itemsBySeller[l.SellerID] = append(itemsBySeller[l.SellerID], OrderItem{
			ListingID: l.ID,
			SellerID:  l.SellerID,
			Title:     l.Title,
			Quantity:  qty,
			PriceKobo: l.PriceKobo,
		})
	}

	if len(itemsBySeller) == 0 {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/cart?unavailable="+strconv.Itoa(skipped), http.StatusSeeOther)
		return
	}

	groupID := newID()
	orders := make([]*Order, 0, len(itemsBySeller))
	for sellerID, items := range itemsBySeller {
		total := 0
		for _, it := range items {
			total += it.PriceKobo * it.Quantity
		}
		fee := int(float64(total) * commission)
		order := &Order{
			ID:               newID(),
			CheckoutGroupID:  groupID,
			BuyerID:          buyer.ID,
			SellerID:         sellerID,
			TotalKobo:        total,
			PlatformFeeKobo:  fee,
			SellerPayoutKobo: total - fee,
			Status:           "pending_payment",
			Items:            items,
			CreatedAt:        time.Now(),
		}
		a.store.Orders[order.ID] = order
		orders = append(orders, order)
	}
	a.store.save()
	a.store.mu.Unlock()

	writeCart(w, remainingCart) // items we could process are gone from the cart; skipped ones stay

	// Dev mode: no Flutterwave keys configured, so skip real payment and
	// mark every order paid directly (through the same finalizeOrderPaid
	// path real confirmations use, so stock still decrements correctly).
	// Remove reliance on this once you deploy with real keys - see the
	// "Configured" check in payment.go.
	if !a.payments.Configured() {
		for _, order := range orders {
			a.finalizeOrderPaid(order, "dev-mode")
		}
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}

	a.startOrderPayment(w, r, buyer, orders[0])
}

// startOrderPayment initiates (or re-initiates) a Flutterwave payment for
// one pending order and redirects the buyer there. Used both right after
// checkout and from the "Pay now" button on /orders for any order that's
// still pending_payment (including a seller-group's later orders, or a
// retry after a failed attempt).
func (a *App) startOrderPayment(w http.ResponseWriter, r *http.Request, buyer *User, order *Order) {
	link, err := a.initiateFlutterwavePayment(order, buyer)
	if err != nil {
		log.Printf("checkout: could not initiate payment for order %s: %v", order.ID, err)
		a.markOrderFailed(order.ID)
		render(w, "checkout_error.html", map[string]any{"User": buyer})
		return
	}
	http.Redirect(w, r, link, http.StatusSeeOther)
}

// handlePayOrder lets a buyer (re)start payment for one of their own
// pending_payment orders - the "Pay now" button on /orders.
func (a *App) handlePayOrder(w http.ResponseWriter, r *http.Request, buyer *User) {
	id := r.URL.Query().Get("id")
	a.store.mu.RLock()
	order, ok := a.store.Orders[id]
	a.store.mu.RUnlock()

	if !ok || order.BuyerID != buyer.ID || order.Status != "pending_payment" {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}
	if !a.payments.Configured() {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}
	a.startOrderPayment(w, r, buyer, order)
}

// handleOrderRecheck lets a buyer manually re-verify one of their own
// orders against Flutterwave directly - the fix for any order that got
// wrongly marked payment_failed by an older version of the callback/webhook
// logic (see isTerminalFailure in payment.go), and a safety net for async
// payment methods like bank transfer where confirmation can lag behind.
func (a *App) handleOrderRecheck(w http.ResponseWriter, r *http.Request, buyer *User) {
	id := r.URL.Query().Get("id")
	a.store.mu.RLock()
	order, ok := a.store.Orders[id]
	a.store.mu.RUnlock()

	if !ok || order.BuyerID != buyer.ID {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}
	if !a.payments.Configured() {
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}

	if err := a.confirmOrderPaidByRef(order.ID); err != nil {
		log.Printf("manual recheck: order %s still not confirmed paid: %v", order.ID, err)
	}
	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

func (a *App) handleOrdersGet(w http.ResponseWriter, r *http.Request, buyer *User) {
	a.store.mu.RLock()
	mine := make([]*Order, 0)
	sellerNames := map[string]string{}
	for _, o := range a.store.Orders {
		if o.BuyerID == buyer.ID {
			mine = append(mine, o)
			if _, ok := sellerNames[o.SellerID]; !ok {
				if s, ok := a.store.Users[o.SellerID]; ok {
					sellerNames[o.SellerID] = s.Name
				}
			}
		}
	}
	a.store.mu.RUnlock()
	sort.Slice(mine, func(i, j int) bool { return mine[i].CreatedAt.After(mine[j].CreatedAt) })

	render(w, "orders.html", map[string]any{
		"Orders":             mine,
		"SellerNames":        sellerNames,
		"PaymentsConfigured": a.payments.Configured(),
		"User":               buyer,
	})
}
