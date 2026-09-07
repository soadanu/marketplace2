package main

import (
	"net/http"
	"strings"
	"time"
)

const sessionCookieName = "kaya_session"
const sessionDuration = 7 * 24 * time.Hour

func (a *App) handleRegisterGet(w http.ResponseWriter, r *http.Request) {
	render(w, "register.html", nil)
}

func (a *App) handleRegisterPost(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	phone := strings.TrimSpace(r.FormValue("phone"))
	password := r.FormValue("password")

	if name == "" || email == "" || len(password) < 6 {
		render(w, "register.html", map[string]string{"Error": "Name, a valid email, and a password of at least 6 characters are required."})
		return
	}

	a.store.mu.Lock()
	for _, u := range a.store.Users {
		if u.Email == email {
			a.store.mu.Unlock()
			render(w, "register.html", map[string]string{"Error": "That email is already registered."})
			return
		}
	}
	user := &User{
		ID:           newID(),
		Name:         name,
		Email:        email,
		Phone:        phone,
		PasswordHash: hashPassword(password),
		Role:         "buyer",
		SellerStatus: "",
		CreatedAt:    time.Now(),
	}
	a.store.Users[user.ID] = user
	a.store.save()
	a.store.mu.Unlock()

	a.startSession(w, user.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	render(w, "login.html", nil)
}

func (a *App) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")

	a.store.mu.RLock()
	var found *User
	for _, u := range a.store.Users {
		if u.Email == email {
			found = u
			break
		}
	}
	a.store.mu.RUnlock()

	if found == nil || !verifyPassword(password, found.PasswordHash) {
		render(w, "login.html", map[string]string{"Error": "Invalid email or password."})
		return
	}

	a.startSession(w, found.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.store.mu.Lock()
		delete(a.store.Sessions, c.Value)
		a.store.save()
		a.store.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) startSession(w http.ResponseWriter, userID string) {
	token := newToken()
	sess := &Session{Token: token, UserID: userID, ExpiresAt: time.Now().Add(sessionDuration)}

	a.store.mu.Lock()
	a.store.Sessions[token] = sess
	a.store.save()
	a.store.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// currentUser returns the logged-in user for this request, or nil.
func (a *App) currentUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()

	sess, ok := a.store.Sessions[c.Value]
	if !ok || time.Now().After(sess.ExpiresAt) {
		return nil
	}
	return a.store.Users[sess.UserID]
}

// requireLogin wraps a handler so it redirects to /login when no session exists.
func (a *App) requireLogin(next func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r, user)
	}
}

// requireAdmin wraps a handler so only Role == "admin" can reach it.
func (a *App) requireAdmin(next func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return a.requireLogin(func(w http.ResponseWriter, r *http.Request, user *User) {
		if user.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r, user)
	})
}

// requireApprovedSeller wraps a handler so only approved sellers can reach it.
func (a *App) requireApprovedSeller(next func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return a.requireLogin(func(w http.ResponseWriter, r *http.Request, user *User) {
		if user.SellerStatus != "approved" {
			http.Error(w, "you must be an approved seller to do this", http.StatusForbidden)
			return
		}
		next(w, r, user)
	})
}
