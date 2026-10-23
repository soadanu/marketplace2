package main

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

var nigerianStates = []string{
	"Abia", "Adamawa", "Akwa Ibom", "Anambra", "Bauchi", "Bayelsa", "Benue", "Borno",
	"Cross River", "Delta", "Ebonyi", "Edo", "Ekiti", "Enugu", "FCT (Abuja)", "Gombe", "Imo",
	"Jigawa", "Kaduna", "Kano", "Katsina", "Kebbi", "Kogi", "Kwara", "Lagos", "Nasarawa",
	"Niger", "Ogun", "Ondo", "Osun", "Oyo", "Plateau", "Rivers", "Sokoto", "Taraba", "Yobe", "Zamfara",
}

// shippingInfo is where an order should be sent. Each order keeps its own
// copy, so changing your saved address later never alters past orders.
type shippingInfo struct {
	Name, Phone, Address, City, State string
}

func shippingFromUser(u *User) shippingInfo {
	return shippingInfo{Name: u.ShipName, Phone: u.Phone, Address: u.ShipAddress, City: u.ShipCity, State: u.ShipState}
}

func digitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func validState(s string) bool {
	for _, st := range nigerianStates {
		if st == s {
			return true
		}
	}
	return false
}

// parseShipping reads and validates the delivery fields of a submitted form.
// The second return value is an error message for the buyer, empty if valid.
func parseShipping(r *http.Request) (shippingInfo, string) {
	s := shippingInfo{
		Name:    strings.TrimSpace(r.FormValue("ship_name")),
		Phone:   strings.TrimSpace(r.FormValue("ship_phone")),
		Address: strings.TrimSpace(r.FormValue("ship_address")),
		City:    strings.TrimSpace(r.FormValue("ship_city")),
		State:   strings.TrimSpace(r.FormValue("ship_state")),
	}
	switch {
	case s.Name == "" || s.Phone == "" || s.Address == "" || s.City == "" || s.State == "":
		return s, "Please fill in your name, phone number, address, city and state so the seller knows where to send your order."
	case len(s.Name) > 100 || len(s.Address) > 300 || len(s.City) > 100:
		return s, "One of the delivery fields is too long."
	case digitCount(s.Phone) < 7 || len(s.Phone) > 20:
		return s, "Enter a phone number the seller or courier can reach you on."
	case !validState(s.State):
		return s, "Choose your state from the list."
	}
	return s, ""
}

func (o *Order) setShipping(s shippingInfo) {
	o.ShipName, o.ShipPhone, o.ShipAddress, o.ShipCity, o.ShipState = s.Name, s.Phone, s.Address, s.City, s.State
}

// saveShippingToProfile remembers the address so the next checkout is prefilled.
func (a *App) saveShippingToProfile(u *User, s shippingInfo) {
	a.store.mu.Lock()
	u.ShipName, u.ShipAddress, u.ShipCity, u.ShipState = s.Name, s.Address, s.City, s.State
	if u.Phone == "" {
		u.Phone = s.Phone
	}
	a.store.save()
	a.store.mu.Unlock()
}

type checkoutLine struct {
	Title    string
	Image    string // file name of the first photo, "" if none
	Qty      int
	Subtotal int
}

func (a *App) renderCheckout(w http.ResponseWriter, buyer *User, action string, hidden map[string]string, lines []checkoutLine, total int, ship shippingInfo, msg string) {
	render(w, "checkout.html", map[string]any{
		"User":   buyer,
		"Action": action,
		"Hidden": hidden,
		"Lines":  lines,
		"Total":  total,
		"Ship":   ship,
		"States": nigerianStates,
		"Error":  msg,
	})
}

// renderCheckoutFromCart shows the delivery form for the current cart.
func (a *App) renderCheckoutFromCart(w http.ResponseWriter, r *http.Request, buyer *User, ship shippingInfo, msg string) {
	c := readCart(r)
	a.store.mu.RLock()
	lines := make([]checkoutLine, 0, len(c))
	total := 0
	for id, qty := range c {
		l, ok := a.store.Listings[id]
		if !ok || l.Status != "active" {
			continue
		}
		lines = append(lines, checkoutLine{Title: l.Title, Image: listingCover(l), Qty: qty, Subtotal: l.PriceKobo * qty})
		total += l.PriceKobo * qty
	}
	a.store.mu.RUnlock()
	if len(lines) == 0 {
		http.Redirect(w, r, "/cart", http.StatusSeeOther)
		return
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Title < lines[j].Title })
	a.renderCheckout(w, buyer, "/checkout", nil, lines, total, ship, msg)
}

func (a *App) handleCheckoutGet(w http.ResponseWriter, r *http.Request, buyer *User) {
	a.renderCheckoutFromCart(w, r, buyer, shippingFromUser(buyer), "")
}

// renderBuyNowPage shows the delivery form for a single "Buy now" item.
func (a *App) renderBuyNowPage(w http.ResponseWriter, r *http.Request, buyer *User, ship shippingInfo, msg string) {
	listingID := r.FormValue("listing_id")
	qty, err := strconv.Atoi(r.FormValue("qty"))
	if err != nil || qty < 1 {
		qty = 1
	}

	a.store.mu.RLock()
	l, ok := a.store.Listings[listingID]
	var seller *User
	var title, image string
	var price, stock int
	var status string
	if ok {
		seller = a.store.Users[l.SellerID]
		title, image, price, stock, status = l.Title, listingCover(l), l.PriceKobo, l.Stock, l.Status
	}
	a.store.mu.RUnlock()

	if !ok || seller == nil || seller.Suspended || status != "active" || stock < qty ||
		(a.payments.Configured() && seller.FlutterwaveSubaccountID == "") {
		http.Redirect(w, r, "/listing?id="+url.QueryEscape(listingID), http.StatusSeeOther)
		return
	}
	lines := []checkoutLine{{Title: title, Image: image, Qty: qty, Subtotal: price * qty}}
	a.renderCheckout(w, buyer, "/buy-now", map[string]string{"listing_id": listingID, "qty": strconv.Itoa(qty)}, lines, price*qty, ship, msg)
}
