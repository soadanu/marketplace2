package main

import (
	"net/http"
	"strings"
)

func (a *App) renderAccount(w http.ResponseWriter, u *User, profileMsg, profileErr, pwMsg, pwErr string) {
	a.store.mu.RLock()
	view := *u // copy so the template reads a stable snapshot
	a.store.mu.RUnlock()
	render(w, "account.html", map[string]any{
		"User": &view, "States": nigerianStates,
		"ProfileInfo": profileMsg, "ProfileError": profileErr, "PwInfo": pwMsg, "PwError": pwErr,
	})
}

func (a *App) handleAccountGet(w http.ResponseWriter, r *http.Request, u *User) {
	a.renderAccount(w, u, "", "", "", "")
}

func (a *App) handleAccountPost(w http.ResponseWriter, r *http.Request, u *User) {
	name := strings.TrimSpace(r.FormValue("name"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	ship := shippingInfo{
		Name:    strings.TrimSpace(r.FormValue("ship_name")),
		Address: strings.TrimSpace(r.FormValue("ship_address")),
		City:    strings.TrimSpace(r.FormValue("ship_city")),
		State:   strings.TrimSpace(r.FormValue("ship_state")),
	}
	switch {
	case name == "" || len(name) > 100:
		a.renderAccount(w, u, "", "Enter your name (up to 100 characters).", "", "")
		return
	case phone != "" && (digitCount(phone) < 7 || len(phone) > 20):
		a.renderAccount(w, u, "", "Enter a valid phone number.", "", "")
		return
	case ship.State != "" && !validState(ship.State):
		a.renderAccount(w, u, "", "Choose your state from the list.", "", "")
		return
	case len(ship.Name) > 100 || len(ship.Address) > 300 || len(ship.City) > 100:
		a.renderAccount(w, u, "", "One of the delivery fields is too long.", "", "")
		return
	}

	a.store.mu.Lock()
	u.Name, u.Phone = name, phone
	u.ShipName, u.ShipAddress, u.ShipCity, u.ShipState = ship.Name, ship.Address, ship.City, ship.State
	a.store.save()
	a.store.mu.Unlock()
	a.renderAccount(w, u, "Saved.", "", "", "")
}

// handlePasswordChange lets a logged-in user set a new password. It asks for
// the current one, and signs out every OTHER device so a stolen session
// can't outlive the change.
func (a *App) handlePasswordChange(w http.ResponseWriter, r *http.Request, u *User) {
	current, next, confirm := r.FormValue("current_password"), r.FormValue("new_password"), r.FormValue("confirm_password")
	ip := clientIP(r)

	if a.loginBlocked(u.Email, ip) {
		a.renderAccount(w, u, "", "", "", "Too many attempts. Please wait a few minutes and try again.")
		return
	}
	a.store.mu.RLock()
	hash := u.PasswordHash
	a.store.mu.RUnlock()

	switch {
	case !verifyPassword(current, hash):
		a.loginFailed(u.Email, ip)
		a.renderAccount(w, u, "", "", "", "Your current password is not right.")
		return
	case len(next) < 6:
		a.renderAccount(w, u, "", "", "", "The new password must be at least 6 characters.")
		return
	case next != confirm:
		a.renderAccount(w, u, "", "", "", "The two new passwords don't match.")
		return
	}

	keep := ""
	if c, err := r.Cookie(sessionCookieName); err == nil {
		keep = c.Value
	}
	newHash := hashPassword(next)
	a.store.mu.Lock()
	u.PasswordHash = newHash
	for tok, s := range a.store.Sessions {
		if s.UserID == u.ID && tok != keep {
			delete(a.store.Sessions, tok)
		}
	}
	a.store.save()
	a.store.mu.Unlock()
	a.clearLoginFailures(u.Email)
	a.renderAccount(w, u, "", "", "Password changed. Any other devices have been signed out.", "")
}

func (a *App) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	render(w, "privacy.html", map[string]any{"User": a.currentUser(r)})
}

// hasDefaultPassword reports whether the account still uses the built-in
// default admin password, so the dashboard can nag until it is changed.
func (a *App) hasDefaultPassword(u *User) bool {
	a.store.mu.RLock()
	hash := u.PasswordHash
	a.store.mu.RUnlock()
	return verifyPassword("changeme123", hash)
}
