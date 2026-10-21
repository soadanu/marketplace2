package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postForgot(app *App, email string) string {
	form := url.Values{"email": {email}}
	req := httptest.NewRequest(http.MethodPost, "/forgot-password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.handleForgotPasswordPost(rec, req)
	return rec.Body.String()
}

// On a deployed site with no SMTP configured, the reset link must never be
// shown on the page - otherwise anyone could take over any account (admin
// included) just by knowing its email. It may still be shown when running
// locally.
func TestResetLinkNotExposedOnLiveSite(t *testing.T) {
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	loadTemplates()

	app := &App{store: NewStore(), payments: PaymentConfig{BaseURL: "https://kaya.example.com"}}
	app.store.Users["U1"] = &User{ID: "U1", Name: "Ada", Email: "ada@example.com"}

	known := postForgot(app, "ada@example.com")
	unknown := postForgot(app, "nobody@example.com")

	if strings.Contains(known, "reset-password?token=") {
		t.Fatalf("reset link was shown on the page of a deployed site")
	}
	if !strings.Contains(known, resetRequestedMessage) || !strings.Contains(unknown, resetRequestedMessage) {
		t.Fatalf("known and unknown emails must get the same message")
	}

	// local development (no SMTP, no public URL) still shows the link
	local := &App{store: app.store}
	if !strings.Contains(postForgot(local, "ada@example.com"), "reset-password?token=") {
		t.Fatalf("expected the link to be shown when running locally")
	}
}
