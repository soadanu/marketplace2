package main

import (
	"log"
	"net/http"
	"strings"
	"time"
)

const resetTokenDuration = 30 * time.Minute

func (a *App) handleForgotPasswordGet(w http.ResponseWriter, r *http.Request) {
	render(w, "forgot_password.html", nil)
}

// handleForgotPasswordPost creates a reset token. There's no email service
// wired up, so the link is logged to the server console and also shown on
// screen - swap sendResetEmail() for a real mailer before going live.
func (a *App) handleForgotPasswordPost(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

	a.store.mu.Lock()
	var user *User
	for _, u := range a.store.Users {
		if u.Email == email {
			user = u
			break
		}
	}

	// Always show the same message whether or not the email exists,
	// so this endpoint can't be used to check which emails are registered.
	if user == nil {
		a.store.mu.Unlock()
		render(w, "forgot_password.html", map[string]string{"Info": "If that email is registered, a reset link has been generated."})
		return
	}

	reset := &PasswordReset{
		Token:     newToken(),
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(resetTokenDuration),
	}
	a.store.Resets[reset.Token] = reset
	a.store.save()
	a.store.mu.Unlock()

	link := "/reset-password?token=" + reset.Token
	sendResetEmail(user.Email, link)

	render(w, "forgot_password.html", map[string]string{
		"Info": "If that email is registered, a reset link has been generated.",
		"DevLink": link, // shown only because there's no mailer yet - remove in production
	})
}

// sendResetEmail is a stand-in for real email delivery. Replace the body
// with an SMTP call or a provider API (e.g. Termii, SendGrid) later.
func sendResetEmail(to, link string) {
	log.Printf("[password reset] would email %s a link to: %s\n", to, link)
}

func (a *App) handleResetPasswordGet(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	render(w, "reset_password.html", map[string]string{"Token": token})
}

func (a *App) handleResetPasswordPost(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	password := r.FormValue("password")

	if len(password) < 6 {
		render(w, "reset_password.html", map[string]string{"Token": token, "Error": "Password must be at least 6 characters."})
		return
	}

	a.store.mu.Lock()
	reset, ok := a.store.Resets[token]
	if !ok || reset.Used || time.Now().After(reset.ExpiresAt) {
		a.store.mu.Unlock()
		render(w, "reset_password.html", map[string]string{"Error": "This reset link is invalid or has expired. Request a new one."})
		return
	}

	user, ok := a.store.Users[reset.UserID]
	if !ok {
		a.store.mu.Unlock()
		http.Error(w, "user not found", http.StatusInternalServerError)
		return
	}

	user.PasswordHash = hashPassword(password)
	reset.Used = true

	// Invalidate all existing sessions for this user - a password reset
	// should log out anyone using the old credentials, including an attacker.
	for token, sess := range a.store.Sessions {
		if sess.UserID == user.ID {
			delete(a.store.Sessions, token)
		}
	}
	a.store.save()
	a.store.mu.Unlock()

	render(w, "login.html", map[string]string{"Info": "Password updated. Log in with your new password."})
}
