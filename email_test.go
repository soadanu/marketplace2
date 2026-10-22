package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func brevoApp(t *testing.T, handler http.HandlerFunc) *App {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := newTestApp(t)
	a.email = EmailConfig{BrevoKey: "test-key", BrevoURL: srv.URL, From: "Kaya Market <orders@kaya.example>"}
	return a
}

func TestBrevoRequestShape(t *testing.T) {
	var gotKey string
	var got map[string]any
	a := brevoApp(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(201)
		io.WriteString(w, `{"messageId":"x"}`)
	})
	if err := a.deliverEmail("buyer@example.com", "Your order has shipped", "On its way"); err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-key" {
		t.Fatalf("api-key header = %q", gotKey)
	}
	sender := got["sender"].(map[string]any)
	to := got["to"].([]any)[0].(map[string]any)
	if sender["email"] != "orders@kaya.example" || sender["name"] != "Kaya Market" {
		t.Fatalf("sender wrong: %v", sender)
	}
	if to["email"] != "buyer@example.com" || got["subject"] != "Your order has shipped" || got["textContent"] != "On its way" {
		t.Fatalf("payload wrong: %v", got)
	}
}

func TestBrevoErrorIsReportedClearly(t *testing.T) {
	a := brevoApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"code":"invalid_parameter","message":"Sender is not valid"}`)
	})
	err := a.deliverEmail("x@example.com", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "Sender is not valid") {
		t.Fatalf("expected the status and Brevo's reason, got %v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatal("the API key must never appear in an error message")
	}
}

func TestEmailModes(t *testing.T) {
	cases := []struct {
		cfg  EmailConfig
		mode string
	}{
		{EmailConfig{}, "none"},
		{EmailConfig{BrevoKey: "k"}, "none"}, // no sender address yet
		{EmailConfig{BrevoKey: "k", From: "a@b.co"}, "brevo"},
		{EmailConfig{Host: "h", Port: "587", Username: "u", Password: "p", From: "a@b.co"}, "smtp"},
		{EmailConfig{BrevoKey: "k", Host: "h", Port: "587", Username: "u", Password: "p", From: "a@b.co"}, "brevo"},
	}
	for _, c := range cases {
		if got := c.cfg.Mode(); got != c.mode || (c.mode != "none") != c.cfg.Configured() {
			t.Errorf("%+v: mode %q configured %v, want %q", c.cfg, got, c.cfg.Configured(), c.mode)
		}
	}
}

// A slow mail service must not make the caller wait: sendEmail is called
// while payments are being confirmed and from the password-reset page.
func TestSendEmailReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	a := brevoApp(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(201)
		close(done)
	})
	start := time.Now()
	a.sendEmail("x@example.com", "s", "b")
	if time.Since(start) > 150*time.Millisecond {
		t.Fatalf("sendEmail blocked for %v", time.Since(start))
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the email was never actually sent")
	}
}

func TestEmailRefusesLineBreaks(t *testing.T) {
	a := brevoApp(t, func(w http.ResponseWriter, r *http.Request) { t.Error("must not send"); w.WriteHeader(201) })
	if err := a.deliverEmail("a@example.com\r\nBcc: evil@example.com", "s", "b"); err == nil {
		t.Fatal("a recipient with a line break must be refused")
	}
	if err := a.deliverEmail("a@example.com", "hi\r\nBcc: evil@example.com", "b"); err == nil {
		t.Fatal("a subject with a line break must be refused")
	}
}

func TestRegisterNeedsRealEmail(t *testing.T) {
	a := newTestApp(t)
	for _, bad := range []string{"adaexample.com", "ada@", "ada@@example.com", "Ada <ada@example.com>", "a b@example.com"} {
		form := url.Values{"name": {"Ada"}, "email": {bad}, "password": {"secret123"}, "agree_terms": {"on"}}
		rec := httptest.NewRecorder()
		a.handleRegisterPost(rec, formReq("POST", "/register", form))
		if len(a.store.Users) != 0 || !strings.Contains(rec.Body.String(), "valid email") {
			t.Fatalf("%q was accepted as an email address", bad)
		}
	}
	form := url.Values{"name": {"Ada"}, "email": {"ada@example.com"}, "password": {"secret123"}, "agree_terms": {"on"}}
	if rec := httptest.NewRecorder(); true {
		a.handleRegisterPost(rec, formReq("POST", "/register", form))
		if rec.Code != http.StatusSeeOther || len(a.store.Users) != 1 {
			t.Fatalf("a good address was refused (%d)", rec.Code)
		}
	}
}

func TestChangeEmail(t *testing.T) {
	a := newTestApp(t)
	u := addUser(a, "U1", "Ada", "buyer")
	addUser(a, "U2", "Bola", "buyer") // u2@example.com

	change := func(newEmail, pw string) string {
		rec := httptest.NewRecorder()
		a.handleEmailChange(rec, formReq("POST", "/account/email", url.Values{"new_email": {newEmail}, "current_password": {pw}}), u)
		return rec.Body.String()
	}
	if body := change("ada@new.example", "wrong"); u.Email != "u1@example.com" || !strings.Contains(body, "current password is not right") {
		t.Fatal("email changed without the right password")
	}
	if change("not-an-email", "secret123"); u.Email != "u1@example.com" {
		t.Fatal("an invalid address was accepted")
	}
	if body := change("u2@example.com", "secret123"); u.Email != "u1@example.com" || !strings.Contains(body, "already belongs") {
		t.Fatal("took another account's email address")
	}
	if change("  ADA@New.Example ", "secret123"); u.Email != "ada@new.example" {
		t.Fatalf("expected a tidied, lowercase address, got %q", u.Email)
	}
	// the new address works for login and the old one no longer does
	login := func(email string) string {
		rec := httptest.NewRecorder()
		a.handleLoginPost(rec, formReq("POST", "/login", url.Values{"email": {email}, "password": {"secret123"}}))
		return rec.Body.String()
	}
	if !strings.Contains(login("u1@example.com"), "Invalid email or password") {
		t.Fatal("the old address should no longer log in")
	}
}

func TestAdminEmailTestReportsResult(t *testing.T) {
	admin := &User{ID: "A1", Role: "admin"}
	send := func(a *App, to string) *emailTestResult {
		a.handleAdminEmailTest(httptest.NewRecorder(), formReq("POST", "/admin/email-test", url.Values{"to": {to}}), admin)
		return a.emailTest.get()
	}

	off := newTestApp(t)
	if r := send(off, "me@example.com"); r.OK || !strings.Contains(r.Msg, "isn't set up") {
		t.Fatalf("unconfigured should say so, got %+v", r)
	}

	good := brevoApp(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	if r := send(good, "me@example.com"); !r.OK || !strings.Contains(r.Msg, "me@example.com") {
		t.Fatalf("expected success, got %+v", r)
	}
	if r := send(good, "nonsense"); r.OK {
		t.Fatal("an invalid address must not be sent to")
	}

	bad := brevoApp(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"message":"Key not found"}`)
	})
	if r := send(bad, "me@example.com"); r.OK || !strings.Contains(r.Msg, "Key not found") {
		t.Fatalf("a rejected key should show Brevo's reason, got %+v", r)
	}
}
