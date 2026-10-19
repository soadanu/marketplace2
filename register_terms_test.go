package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postRegister(app *App, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.handleRegisterPost(rec, req)
	return rec
}

// The terms checkbox is "required" in the form, but a browser check can be
// bypassed - the server must refuse an account unless it was ticked.
func TestRegisterRequiresTermsAgreement(t *testing.T) {
	oldDir := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = oldDir }()
	loadTemplates()

	app := &App{store: NewStore()}
	form := url.Values{"name": {"Ada"}, "email": {"ada@example.com"}, "password": {"secret123"}}

	rec := postRegister(app, form)
	if len(app.store.Users) != 0 {
		t.Fatalf("account was created without agreeing to the terms")
	}
	if !strings.Contains(rec.Body.String(), "agree to the Terms") {
		t.Fatalf("expected an error asking the user to agree to the terms")
	}

	form.Set("agree_terms", "on")
	rec = postRegister(app, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after a valid registration, got %d", rec.Code)
	}
	if len(app.store.Users) != 1 {
		t.Fatalf("expected one user, got %d", len(app.store.Users))
	}
	for _, u := range app.store.Users {
		if u.TermsAcceptedAt.IsZero() {
			t.Fatalf("expected TermsAcceptedAt to be recorded")
		}
	}
}
