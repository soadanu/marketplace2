package main

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
)

// EmailConfig holds SMTP credentials, read from environment variables.
// Works with any standard SMTP provider - Gmail (with an app password),
// SendGrid, Mailgun, Zoho, etc. - since it uses the plain net/smtp package
// rather than a provider-specific SDK, keeping the zero-dependency
// approach. Without these set, emails are logged to the console instead of
// sent, same dev-mode pattern used elsewhere (Flutterwave, etc).
type EmailConfig struct {
	Host     string // e.g. smtp.gmail.com
	Port     string // e.g. 587
	Username string
	Password string
	From     string // e.g. "Kaya Marketplace <no-reply@yourdomain.com>"
}

func loadEmailConfig() EmailConfig {
	return EmailConfig{
		Host:     os.Getenv("SMTP_HOST"),
		Port:     os.Getenv("SMTP_PORT"),
		Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     os.Getenv("SMTP_FROM"),
	}
}

func (e EmailConfig) Configured() bool {
	return e.Host != "" && e.Port != "" && e.Username != "" && e.Password != "" && e.From != ""
}

// sendEmail sends a plain-text email via SMTP, or logs it to the console if
// SMTP isn't configured (dev mode). Errors are logged, not returned to the
// caller - a failed notification email should never break the user-facing
// action that triggered it (e.g. a password reset should still show its
// on-screen confirmation even if the email itself fails to send).
func (a *App) sendEmail(to, subject, body string) {
	if !a.email.Configured() {
		log.Printf("[email - dev mode, not sent] to=%s subject=%q\n%s", to, subject, body)
		return
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		a.email.From, to, subject, body)

	auth := smtp.PlainAuth("", a.email.Username, a.email.Password, a.email.Host)
	addr := a.email.Host + ":" + a.email.Port

	if err := smtp.SendMail(addr, auth, a.email.Username, []string{to}, []byte(msg)); err != nil {
		log.Printf("sendEmail: failed to send to %s: %v", to, err)
	}
}
