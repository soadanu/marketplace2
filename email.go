package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"
)

const brevoEndpoint = "https://api.brevo.com/v3/smtp/email"

// EmailConfig says how Kaya sends email. There are two ways, and the first
// one that is fully set up wins:
//
//  1. Brevo (BREVO_API_KEY + EMAIL_FROM). Talks to Brevo over HTTPS, which is
//     the way to send from Render's free plan: Render blocks the SMTP ports
//     (25, 465, 587) there.
//  2. Plain SMTP (SMTP_HOST, SMTP_PORT, SMTP_USERNAME, SMTP_PASSWORD and
//     EMAIL_FROM or SMTP_FROM). Works on hosts that allow SMTP, such as a
//     paid Render plan, a VPS, or your own computer.
//
// With neither set, emails are written to the server log instead of sent.
type EmailConfig struct {
	BrevoKey string
	BrevoURL string // BREVO_API_URL overrides it, for testing against a fake server

	Host     string // e.g. smtp.gmail.com
	Port     string // e.g. 587
	Username string
	Password string

	From string // e.g. "Kaya <orders@yourdomain.com>"
}

func loadEmailConfig() EmailConfig {
	from := os.Getenv("EMAIL_FROM")
	if from == "" {
		from = os.Getenv("SMTP_FROM") // the older name for the same setting
	}
	url := os.Getenv("BREVO_API_URL")
	if url == "" {
		url = brevoEndpoint
	}
	return EmailConfig{
		BrevoKey: os.Getenv("BREVO_API_KEY"),
		BrevoURL: url,
		Host:     os.Getenv("SMTP_HOST"),
		Port:     os.Getenv("SMTP_PORT"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     from,
	}
}

func (e EmailConfig) brevoReady() bool { return e.BrevoKey != "" && e.From != "" }
func (e EmailConfig) smtpReady() bool {
	return e.Host != "" && e.Port != "" && e.Username != "" && e.Password != "" && e.From != ""
}

// Configured reports whether Kaya has any real way to send email.
func (e EmailConfig) Configured() bool { return e.brevoReady() || e.smtpReady() }

// Mode names the method in use: "brevo", "smtp" or "none".
func (e EmailConfig) Mode() string {
	switch {
	case e.brevoReady():
		return "brevo"
	case e.smtpReady():
		return "smtp"
	}
	return "none"
}

var errNoEmail = errors.New("email is not set up (set BREVO_API_KEY and EMAIL_FROM)")

// deliverEmail sends one plain-text email right now and reports what went
// wrong, if anything. Most code should call sendEmail instead.
func (a *App) deliverEmail(to, subject, body string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("refusing to send: address or subject contains a line break")
	}
	switch {
	case a.email.brevoReady():
		return a.sendViaBrevo(to, subject, body)
	case a.email.smtpReady():
		return a.sendViaSMTP(to, subject, body)
	}
	return errNoEmail
}

func (a *App) sendViaBrevo(to, subject, body string) error {
	from, err := mail.ParseAddress(a.email.From)
	if err != nil {
		return fmt.Errorf("EMAIL_FROM is not a valid address: %w", err)
	}
	sender := map[string]string{"email": from.Address}
	if from.Name != "" {
		sender["name"] = from.Name
	}
	payload, _ := json.Marshal(map[string]any{
		"sender":      sender,
		"to":          []map[string]string{{"email": to}},
		"subject":     subject,
		"textContent": body,
	})
	req, err := http.NewRequest(http.MethodPost, a.email.BrevoURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", a.email.BrevoKey)
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Brevo answered %d: %s", resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	return nil
}

func (a *App) sendViaSMTP(to, subject, body string) error {
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		a.email.From, to, mime.QEncoding.Encode("UTF-8", subject), body)
	auth := smtp.PlainAuth("", a.email.Username, a.email.Password, a.email.Host)
	return smtp.SendMail(a.email.Host+":"+a.email.Port, auth, a.email.Username, []string{to}, []byte(msg))
}

// sendEmail sends in the background and never makes the caller wait. That
// matters: it is called while confirming payments (when the whole data store
// is locked) and from the password-reset page, and a slow mail service must
// not freeze the site or make that page's timing reveal which emails exist.
// Failures are logged, not shown - a failed notification should never break
// the action that triggered it.
//
// With email not set up, the message is written to the server log instead,
// which is also how you recover a password reset link in that situation.
func (a *App) sendEmail(to, subject, body string) {
	if !a.email.Configured() {
		log.Printf("[email - not set up, so not sent] to=%s subject=%q\n%s", to, subject, body)
		return
	}
	go func() {
		if err := a.deliverEmail(to, subject, body); err != nil {
			log.Printf("sendEmail: could not send %q to %s: %v", subject, to, err)
		}
	}()
}

// --- admin "send a test email" ---

type emailTestResult struct {
	At  time.Time
	OK  bool
	Msg string
}

type emailTestState struct {
	mu   sync.Mutex
	last *emailTestResult
}

func (s *emailTestState) set(r *emailTestResult) { s.mu.Lock(); s.last = r; s.mu.Unlock() }
func (s *emailTestState) get() *emailTestResult  { s.mu.Lock(); defer s.mu.Unlock(); return s.last }

// handleAdminEmailTest sends a real email right away and keeps the exact
// result, so a wrong key or sender shows up on the dashboard instead of
// only in the server log.
func (a *App) handleAdminEmailTest(w http.ResponseWriter, r *http.Request, admin *User) {
	to := strings.TrimSpace(r.FormValue("to"))
	res := &emailTestResult{At: time.Now()}
	addr, err := mail.ParseAddress(to)
	switch {
	case err != nil || addr.Address != to:
		res.Msg = "That doesn't look like an email address."
	case !a.email.Configured():
		res.Msg = "Email isn't set up yet, so nothing was sent. Set BREVO_API_KEY and EMAIL_FROM, then redeploy."
	default:
		if err := a.deliverEmail(to, "Kaya test email", "If you can read this, Kaya can send email.\n\nPassword reset links and order updates will reach people the same way."); err != nil {
			res.Msg = "Sending failed: " + err.Error()
		} else {
			res.OK = true
			res.Msg = "Sent to " + to + ". Check that inbox, and the spam folder too."
		}
	}
	a.emailTest.set(res)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
