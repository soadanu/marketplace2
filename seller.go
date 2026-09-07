package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

func (a *App) handleSellerApplyGet(w http.ResponseWriter, r *http.Request, user *User) {
	render(w, "seller_apply.html", map[string]any{"User": user})
}

func (a *App) handleSellerApplyPost(w http.ResponseWriter, r *http.Request, user *User) {
	if user.SellerStatus == "pending" || user.SellerStatus == "approved" {
		http.Redirect(w, r, "/seller/apply", http.StatusSeeOther)
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "form too large or malformed", http.StatusBadRequest)
		return
	}

	nin := strings.TrimSpace(r.FormValue("nin"))
	altType := strings.TrimSpace(r.FormValue("alt_id_type"))
	altValue := strings.TrimSpace(r.FormValue("alt_id_value"))

	if len(nin) != 11 {
		http.Error(w, "NIN must be 11 digits", http.StatusBadRequest)
		return
	}

	// NIN document image is stored in a KYC-only folder, never served publicly.
	ninDocPath, err := saveUploadedFile(r, "nin_doc", "data/uploads/kyc")
	if err != nil && err != http.ErrMissingFile {
		http.Error(w, "could not save ID document: "+err.Error(), http.StatusBadRequest)
		return
	}

	app := &SellerApplication{
		ID:          newID(),
		UserID:      user.ID,
		NIN:         nin,
		NINDocPath:  ninDocPath,
		AltIDType:   altType,
		AltIDValue:  altValue,
		Status:      "pending",
		SubmittedAt: time.Now(),
	}

	a.store.mu.Lock()
	a.store.Applications[app.ID] = app
	if u, ok := a.store.Users[user.ID]; ok {
		u.SellerStatus = "pending"
	}
	a.store.save()
	a.store.mu.Unlock()

	http.Redirect(w, r, "/seller/apply", http.StatusSeeOther)
}

// --- Admin review ---

func (a *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request, admin *User) {
	a.store.mu.RLock()
	pending := make([]*SellerApplication, 0)
	for _, app := range a.store.Applications {
		if app.Status == "pending" {
			pending = append(pending, app)
		}
	}
	applicants := map[string]*User{}
	for _, app := range pending {
		applicants[app.UserID] = a.store.Users[app.UserID]
	}
	a.store.mu.RUnlock()

	sort.Slice(pending, func(i, j int) bool { return pending[i].SubmittedAt.Before(pending[j].SubmittedAt) })

	render(w, "admin.html", map[string]any{
		"Applications": pending,
		"Applicants":   applicants,
		"Categories":   a.sortedCategories(),
		"User":         admin,
	})
}

func (a *App) handleAdminApplicationDecision(w http.ResponseWriter, r *http.Request, admin *User) {
	appID := r.FormValue("id")
	decision := r.FormValue("decision") // "approved" or "rejected"
	if decision != "approved" && decision != "rejected" {
		http.Error(w, "invalid decision", http.StatusBadRequest)
		return
	}

	a.store.mu.Lock()
	app, ok := a.store.Applications[appID]
	if ok {
		app.Status = decision
		app.ReviewedBy = admin.ID
		if u, ok := a.store.Users[app.UserID]; ok {
			u.SellerStatus = decision
			if decision == "approved" {
				u.Role = "seller"
			}
		}
		a.store.save()
	}
	a.store.mu.Unlock()

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
