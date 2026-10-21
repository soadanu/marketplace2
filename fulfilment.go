package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type sellerOrderView struct {
	Order     *Order
	BuyerName string
}

type sellerStats struct {
	Sales      int
	ToShip     int
	EarnedKobo int
}

// handleSellerOrders lists every paid order for the seller's items, with
// the delivery address. Buyers' addresses are only shown after payment is
// confirmed, never for orders that are still waiting on payment.
func (a *App) handleSellerOrders(w http.ResponseWriter, r *http.Request, seller *User) {
	a.store.mu.RLock()
	views := make([]sellerOrderView, 0)
	var stats sellerStats
	for _, o := range a.store.Orders {
		if o.SellerID != seller.ID || !o.IsPaid() {
			continue
		}
		name := ""
		if b, ok := a.store.Users[o.BuyerID]; ok {
			name = b.Name
		}
		views = append(views, sellerOrderView{Order: o, BuyerName: name})
		stats.Sales++
		stats.EarnedKobo += o.SellerPayoutKobo
		if o.Status == "paid" {
			stats.ToShip++
		}
	}
	a.store.mu.RUnlock()

	sort.Slice(views, func(i, j int) bool { return views[i].Order.CreatedAt.After(views[j].Order.CreatedAt) })
	render(w, "seller_orders.html", map[string]any{"Orders": views, "Stats": stats, "User": seller})
}

// handleSellerShip marks one of the seller's paid orders as shipped.
func (a *App) handleSellerShip(w http.ResponseWriter, r *http.Request, seller *User) {
	id := r.FormValue("id")
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 200 {
		note = note[:200]
	}

	a.store.mu.Lock()
	order, ok := a.store.Orders[id]
	if !ok || order.SellerID != seller.ID || order.Status != "paid" {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/seller/orders", http.StatusSeeOther)
		return
	}
	order.Status = "shipped"
	order.ShippedAt = time.Now()
	order.TrackingNote = note
	buyer := a.store.Users[order.BuyerID]
	a.store.save()
	a.store.mu.Unlock()

	if buyer != nil {
		body := fmt.Sprintf("Good news - the seller has shipped your order (%s).\n", order.ID)
		if note != "" {
			body += "\nDelivery note from the seller: " + note + "\n"
		}
		body += "\nWhen it arrives, open My Orders on Kaya and tap \"I've received this\"."
		a.sendEmail(buyer.Email, "Your Kaya order has shipped", body)
	}
	http.Redirect(w, r, "/seller/orders", http.StatusSeeOther)
}

// handleOrderReceived lets the buyer confirm a shipped order arrived.
func (a *App) handleOrderReceived(w http.ResponseWriter, r *http.Request, buyer *User) {
	id := r.FormValue("id")

	a.store.mu.Lock()
	order, ok := a.store.Orders[id]
	if !ok || order.BuyerID != buyer.ID || order.Status != "shipped" {
		a.store.mu.Unlock()
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
		return
	}
	order.Status = "delivered"
	order.DeliveredAt = time.Now()
	seller := a.store.Users[order.SellerID]
	a.store.save()
	a.store.mu.Unlock()

	if seller != nil {
		a.sendEmail(seller.Email, "A buyer confirmed delivery",
			fmt.Sprintf("The buyer confirmed that order %s arrived. Thanks for selling on Kaya.", order.ID))
	}
	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}
