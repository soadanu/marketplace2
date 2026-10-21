package main

import (
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

	// Throttle so this page can't be used to flood someone's inbox. The reply
	// is the same neutral message either way.
	ip := clientIP(r)
	if a.resetLimited(email, ip) {
		render(w, "forgot_password.html", map[string]string{"Info": resetRequestedMessage})
		return
	}
	a.recordReset(email, ip)

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
		render(w, "forgot_password.html", map[string]string{"Info": resetRequestedMessage})
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

	link := a.payments.BaseURL + "/reset-password?token=" + reset.Token
	if a.payments.BaseURL == "" {
		link = "/reset-password?token=" + reset.Token // local dev fallback, relative link
	}
	a.sendEmail(user.Email, "Reset your Kaya password",
		"Someone (hopefully you) asked to reset the password on your Kaya account.\n\n"+
			"Click this link to set a new password - it expires in 30 minutes:\n"+link+"\n\n"+
			"If you didn't request this, you can safely ignore this email.")

	info := map[string]string{"Info": resetRequestedMessage}
	if a.isLocalDev() {
		// Local development only: no mailer and no public URL configured, so
		// show the link on screen. NEVER do this on a live site - anyone could
		// then reset any account's password just by knowing its email. Live
		// without SMTP, the link goes to the server log only (see sendEmail).
		info["DevLink"] = link
	}
	render(w, "forgot_password.html", info)
}

// resetRequestedMessage is shown whether or not the email is registered, so
// this page can't be used to find out who has an account.
const resetRequestedMessage = "If that email is registered, a password reset link has been sent to it."

// isLocalDev reports whether the app is running on a developer's machine:
// no SMTP configured and no public URL set. A deployed site always has
// APP_BASE_URL (payments need it), so it is never treated as local.
func (a *App) isLocalDev() bool {
	return !a.email.Configured() && a.payments.BaseURL == ""
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
	a.clearLoginFailures(user.Email) // a successful reset also lifts any login lockout
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
