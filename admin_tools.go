package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type adminStats struct {
	PendingApplications int
	OpenReports         int
	NeedsAttention      int // paid orders flagged as oversold
}

func (a *App) adminCounts() adminStats {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	var s adminStats
	for _, app := range a.store.Applications {
		if app.Status == "pending" {
			s.PendingApplications++
		}
	}
	for _, rep := range a.store.Reports {
		if rep.Status == "open" {
			s.OpenReports++
		}
	}
	for _, o := range a.store.Orders {
		if o.OversoldWarning != "" {
			s.NeedsAttention++
		}
	}
	return s
}

// safeAdminNext only lets a form send the admin back to another admin page,
// so a crafted "next" value can't bounce them to an outside site.
func safeAdminNext(next, fallback string) string {
	if strings.HasPrefix(next, "/admin") && !strings.HasPrefix(next, "//") {
		return next
	}
	return fallback
}

// --- Orders overview ---

type adminOrderRow struct {
	Order      *Order
	BuyerName  string
	SellerName string
}

type adminMoney struct {
	PaidOrders     int
	GrossKobo      int
	CommissionKobo int
	PayoutKobo     int
}

func (a *App) handleAdminOrders(w http.ResponseWriter, r *http.Request, admin *User) {
	onlyAttention := r.URL.Query().Get("attention") == "1"
	a.store.mu.RLock()
	rows := make([]adminOrderRow, 0)
	var m adminMoney
	for _, o := range a.store.Orders {
		if o.IsPaid() {
			m.PaidOrders++
			m.GrossKobo += o.TotalKobo
			m.CommissionKobo += o.PlatformFeeKobo
			m.PayoutKobo += o.SellerPayoutKobo
		}
		if onlyAttention && o.OversoldWarning == "" {
			continue
		}
		row := adminOrderRow{Order: o}
		if b, ok := a.store.Users[o.BuyerID]; ok {
			row.BuyerName = b.Name
		}
		if s, ok := a.store.Users[o.SellerID]; ok {
			row.SellerName = s.Name
		}
		rows = append(rows, row)
	}
	a.store.mu.RUnlock()

	sort.Slice(rows, func(i, j int) bool { return rows[i].Order.CreatedAt.After(rows[j].Order.CreatedAt) })
	if len(rows) > 300 {
		rows = rows[:300]
	}
	render(w, "admin_orders.html", map[string]any{"Rows": rows, "Money": m, "OnlyAttention": onlyAttention, "User": admin})
}

// --- Listings moderation ---

type adminListingRow struct {
	Listing    *Listing
	SellerName string
}

func (a *App) handleAdminListings(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	rows := make([]adminListingRow, 0, len(a.store.Listings))
	for _, l := range a.store.Listings {
		name := ""
		if s, ok := a.store.Users[l.SellerID]; ok {
			name = s.Name
		}
		rows = append(rows, adminListingRow{Listing: l, SellerName: name})
	}
	a.store.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].Listing.CreatedAt.After(rows[j].Listing.CreatedAt) })
	if len(rows) > 300 {
		rows = rows[:300]
	}
	render(w, "admin_listings.html", map[string]any{"Rows": rows, "User": admin})
}

// handleAdminListingStatus removes a listing from the market, or restores it.
func (a *App) handleAdminListingStatus(w http.ResponseWriter, r *http.Request, admin *User) {
	id, action := r.FormValue("id"), r.FormValue("action")
	a.store.mu.Lock()
	if l, ok := a.store.Listings[id]; ok {
		switch action {
		case "remove":
			l.Status = "removed"
		case "restore":
			if l.Status == "removed" {
				if l.Stock > 0 {
					l.Status = "active"
				} else {
					l.Status = "sold_out"
				}
			}
		}
		a.store.save()
	}
	a.store.mu.Unlock()
	http.Redirect(w, r, safeAdminNext(r.FormValue("next"), "/admin/listings"), http.StatusSeeOther)
}

// --- Users ---

func (a *App) handleAdminUsers(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	users := make([]*User, 0, len(a.store.Users))
	for _, u := range a.store.Users {
		users = append(users, u)
	}
	a.store.mu.RUnlock()
	sort.Slice(users, func(i, j int) bool { return users[i].CreatedAt.After(users[j].CreatedAt) })
	render(w, "admin_users.html", map[string]any{"Users": users, "User": admin})
}

// handleAdminUserSuspend suspends or reinstates an account. Admins can't be
// suspended (so you can't lock yourself or another admin out by accident).
func (a *App) handleAdminUserSuspend(w http.ResponseWriter, r *http.Request, admin *User) {
	id, action := r.FormValue("id"), r.FormValue("action")
	a.store.mu.Lock()
	if u, ok := a.store.Users[id]; ok && u.Role != "admin" {
		switch action {
		case "suspend":
			u.Suspended = true
			for tok, s := range a.store.Sessions {
				if s.UserID == u.ID {
					delete(a.store.Sessions, tok)
				}
			}
		case "unsuspend":
			u.Suspended = false
		}
		a.store.save()
	}
	a.store.mu.Unlock()
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

// --- Reports ---

type adminReportRow struct {
	Report       *Report
	ReporterName string
	ListingTitle string
	ListingState string
	SellerName   string
}

func (a *App) handleAdminReports(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	rows := make([]adminReportRow, 0, len(a.store.Reports))
	for _, rep := range a.store.Reports {
		row := adminReportRow{Report: rep}
		if u, ok := a.store.Users[rep.ReporterID]; ok {
			row.ReporterName = u.Name
		}
		if l, ok := a.store.Listings[rep.ListingID]; ok {
			row.ListingTitle, row.ListingState = l.Title, l.Status
		}
		if u, ok := a.store.Users[rep.SellerID]; ok {
			row.SellerName = u.Name
		}
		rows = append(rows, row)
	}
	a.store.mu.RUnlock()
	// open reports first, then newest
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].Report.Status == "open") != (rows[j].Report.Status == "open") {
			return rows[i].Report.Status == "open"
		}
		return rows[i].Report.CreatedAt.After(rows[j].Report.CreatedAt)
	})
	render(w, "admin_reports.html", map[string]any{"Rows": rows, "User": admin})
}

func (a *App) handleAdminReportResolve(w http.ResponseWriter, r *http.Request, admin *User) {
	id := r.FormValue("id")
	a.store.mu.Lock()
	if rep, ok := a.store.Reports[id]; ok && rep.Status == "open" {
		rep.Status = "resolved"
		rep.ResolvedBy = admin.ID
		rep.ResolvedAt = time.Now()
		a.store.save()
	}
	a.store.mu.Unlock()
	http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
}

// --- Seller ID documents ---

// handleAdminKYC shows an applicant's uploaded ID photo to an admin. The
// file is found from the application record, never from a path in the
// request, and served only if its content really is an image.
func (a *App) handleAdminKYC(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	app, ok := a.store.Applications[r.URL.Query().Get("id")]
	path := ""
	if ok {
		path = app.NINDocPath
	}
	a.store.mu.RUnlock()
	if !ok || path == "" {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(filepath.Join(dataPath("uploads", "kyc"), filepath.Base(path)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := f.Read(head)
	ctype := http.DetectContentType(head[:n])
	if _, ok := imageExt(ctype); !ok {
		http.NotFound(w, r)
		return
	}
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
