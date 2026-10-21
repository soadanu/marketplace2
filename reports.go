package main

import (
	"net/http"
	"strings"
	"time"
)

var reportReasons = []string{
	"Scam or fake item",
	"Prohibited or illegal item",
	"Wrong or misleading information",
	"Abusive or rude seller",
	"Other",
}

func validReason(s string) bool {
	for _, r := range reportReasons {
		if r == s {
			return true
		}
	}
	return false
}

// reportTarget describes what is being reported, for the form heading.
func (a *App) reportTarget(listingID, sellerID string) (title string, ok bool) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if listingID != "" {
		if l, found := a.store.Listings[listingID]; found {
			return "the listing \"" + l.Title + "\"", true
		}
		return "", false
	}
	if u, found := a.store.Users[sellerID]; found && sellerID != "" {
		return "the seller " + u.Name, true
	}
	return "", false
}

func (a *App) handleReportGet(w http.ResponseWriter, r *http.Request, user *User) {
	listingID, sellerID := r.URL.Query().Get("listing_id"), r.URL.Query().Get("seller_id")
	target, ok := a.reportTarget(listingID, sellerID)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	render(w, "report.html", map[string]any{
		"User": user, "Target": target, "ListingID": listingID, "SellerID": sellerID, "Reasons": reportReasons,
	})
}

func (a *App) handleReportPost(w http.ResponseWriter, r *http.Request, user *User) {
	listingID, sellerID := r.FormValue("listing_id"), r.FormValue("seller_id")
	reason := r.FormValue("reason")
	details := strings.TrimSpace(r.FormValue("details"))
	if len(details) > 500 {
		details = details[:500]
	}
	target, ok := a.reportTarget(listingID, sellerID)
	if !ok || !validReason(reason) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	a.store.mu.Lock()
	if listingID != "" {
		if l, found := a.store.Listings[listingID]; found {
			sellerID = l.SellerID
		}
	}
	// One open report per person per target - repeat clicks are ignored.
	duplicate := false
	for _, rep := range a.store.Reports {
		if rep.ReporterID == user.ID && rep.Status == "open" && rep.ListingID == listingID && rep.SellerID == sellerID {
			duplicate = true
			break
		}
	}
	if !duplicate {
		rep := &Report{ID: newID(), ReporterID: user.ID, ListingID: listingID, SellerID: sellerID,
			Reason: reason, Details: details, Status: "open", CreatedAt: time.Now()}
		a.store.Reports[rep.ID] = rep
		a.store.save()
	}
	a.store.mu.Unlock()

	render(w, "report.html", map[string]any{"User": user, "Target": target, "Done": true})
}
