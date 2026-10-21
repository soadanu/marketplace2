package main

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 1x1 PNG, enough for the upload sniffing to see a real image.
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func newTestApp(t *testing.T) *App {
	t.Helper()
	old := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = old })
	loadTemplates()
	return &App{store: NewStore(), limits: newLimits()}
}

func formReq(method, target string, vals url.Values) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func addUser(a *App, id, name, role string) *User {
	u := &User{ID: id, Name: name, Email: strings.ToLower(id) + "@example.com", Role: role, PasswordHash: hashPassword("secret123"), CreatedAt: time.Now()}
	if role == "seller" {
		u.SellerStatus = "approved"
	}
	a.store.Users[id] = u
	return u
}

func paidVerify(ref string, naira float64) *flwVerifyResponse {
	v := &flwVerifyResponse{Status: "success"}
	v.Data.TxRef, v.Data.FlwRef, v.Data.Amount, v.Data.Currency, v.Data.Status = ref, "FLW-"+ref, naira, "NGN", "successful"
	return v
}

// Once an order is shipped it is still PAID. A late duplicate webhook or a
// manual recheck must not take stock off a second time, nor flip it to failed.
func TestShippedOrderStaysPaid(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "S1", "Seller", "seller")
	addUser(a, "B1", "Buyer", "buyer")
	a.store.Listings["L1"] = &Listing{ID: "L1", SellerID: "S1", Title: "phone", PriceKobo: 1000, Stock: 5, Status: "active"}
	o := &Order{ID: "O1", BuyerID: "B1", SellerID: "S1", TotalKobo: 1000, Status: "pending_payment",
		Items: []OrderItem{{ListingID: "L1", SellerID: "S1", Title: "phone", Quantity: 1, PriceKobo: 1000}}}
	a.store.Orders["O1"] = o

	if err := a.applyVerifiedTransaction(paidVerify("O1", 10)); err != nil {
		t.Fatal(err)
	}
	o.Status = "shipped"
	if err := a.applyVerifiedTransaction(paidVerify("O1", 10)); err != nil {
		t.Fatal(err)
	}
	a.markOrderFailed("O1")
	if o.Status != "shipped" {
		t.Fatalf("shipped order changed to %q", o.Status)
	}
	if got := a.store.Listings["L1"].Stock; got != 4 {
		t.Fatalf("stock should be 4 (taken off once), got %d", got)
	}
}

func TestLimiter(t *testing.T) {
	var nilLimiter *limiter
	nilLimiter.record("x")
	if nilLimiter.blocked("x") {
		t.Fatal("a nil limiter must never block")
	}

	l := newLimiter(3, 80*time.Millisecond)
	for i := 0; i < 3; i++ {
		l.record("a")
	}
	if !l.blocked("a") || l.blocked("b") {
		t.Fatal("expected only key a to be blocked")
	}
	l.reset("a")
	if l.blocked("a") {
		t.Fatal("reset should clear the block")
	}
	for i := 0; i < 3; i++ {
		l.record("a")
	}
	time.Sleep(120 * time.Millisecond)
	if l.blocked("a") {
		t.Fatal("block should expire after the window")
	}
}

func TestLoginLockoutAndSuspension(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "U1", "Ada", "buyer")
	login := func(email, pw string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.handleLoginPost(rec, formReq("POST", "/login", url.Values{"email": {email}, "password": {pw}}))
		return rec
	}

	if rec := login("u1@example.com", "secret123"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sanity check: the right password should log in, got %d", rec.Code)
	}
	for i := 0; i < 5; i++ {
		if rec := login("u1@example.com", "wrong"); !strings.Contains(rec.Body.String(), "Invalid email or password") {
			t.Fatalf("attempt %d: expected invalid-credentials message", i)
		}
	}
	// Locked now: even the right password is refused.
	if rec := login("u1@example.com", "secret123"); !strings.Contains(rec.Body.String(), "Too many failed attempts") {
		t.Fatalf("expected a lockout message, got: %s", rec.Body.String())
	}

	// A different account is unaffected, and a suspended one cannot log in.
	b := addUser(a, "U2", "Bola", "buyer")
	if rec := login("u2@example.com", "secret123"); rec.Code != http.StatusSeeOther {
		t.Fatalf("expected login to work for another user, got %d", rec.Code)
	}
	b.Suspended = true
	if rec := login("u2@example.com", "secret123"); !strings.Contains(rec.Body.String(), "suspended") {
		t.Fatal("suspended users must not be able to log in")
	}
}

func TestSuspendKillsSessions(t *testing.T) {
	a := newTestApp(t)
	admin := addUser(a, "A1", "Admin", "admin")
	u := addUser(a, "U1", "Ada", "buyer")

	rec := httptest.NewRecorder()
	a.startSession(rec, u.ID)
	cookie := rec.Result().Cookies()[0]
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if a.currentUser(req) == nil {
		t.Fatal("expected a valid session")
	}

	a.handleAdminUserSuspend(httptest.NewRecorder(), formReq("POST", "/admin/users/suspend", url.Values{"id": {u.ID}, "action": {"suspend"}}), admin)
	if a.currentUser(req) != nil {
		t.Fatal("a suspended user's session must stop working")
	}
	// admins can't be suspended
	a.handleAdminUserSuspend(httptest.NewRecorder(), formReq("POST", "/admin/users/suspend", url.Values{"id": {admin.ID}, "action": {"suspend"}}), admin)
	if admin.Suspended {
		t.Fatal("an admin must not be suspendable")
	}
}

func TestParseShipping(t *testing.T) {
	good := url.Values{"ship_name": {"Ada Obi"}, "ship_phone": {"0803 123 4567"}, "ship_address": {"12 Marina Rd"}, "ship_city": {"Ikeja"}, "ship_state": {"Lagos"}}
	if _, msg := parseShipping(formReq("POST", "/", good)); msg != "" {
		t.Fatalf("valid details rejected: %s", msg)
	}
	cases := map[string]func(v url.Values){
		"missing address": func(v url.Values) { v.Set("ship_address", "") },
		"short phone":     func(v url.Values) { v.Set("ship_phone", "123") },
		"unknown state":   func(v url.Values) { v.Set("ship_state", "Atlantis") },
		"huge address":    func(v url.Values) { v.Set("ship_address", strings.Repeat("x", 400)) },
	}
	for name, mutate := range cases {
		v := url.Values{}
		for k, vs := range good {
			v[k] = vs
		}
		mutate(v)
		if _, msg := parseShipping(formReq("POST", "/", v)); msg == "" {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestCheckoutNeedsDeliveryDetails(t *testing.T) {
	a := newTestApp(t) // no Flutterwave keys: dev mode, orders are marked paid at once
	addUser(a, "S1", "Seller", "seller")
	buyer := addUser(a, "B1", "Buyer", "buyer")
	a.store.Listings["L1"] = &Listing{ID: "L1", SellerID: "S1", Title: "phone", PriceKobo: 1000, Stock: 5, Status: "active"}

	cartRec := httptest.NewRecorder()
	writeCart(cartRec, cart{"L1": 2})
	withCart := func(r *http.Request) *http.Request { r.AddCookie(cartRec.Result().Cookies()[0]); return r }

	rec := httptest.NewRecorder()
	a.handleCheckout(rec, withCart(formReq("POST", "/checkout", url.Values{})), buyer)
	if len(a.store.Orders) != 0 || !strings.Contains(rec.Body.String(), "Please fill in your name") {
		t.Fatal("checkout without an address must not create an order")
	}

	form := url.Values{"ship_name": {"Ada Obi"}, "ship_phone": {"08031234567"}, "ship_address": {"12 Marina Rd"}, "ship_city": {"Ikeja"}, "ship_state": {"Lagos"}}
	rec = httptest.NewRecorder()
	a.handleCheckout(rec, withCart(formReq("POST", "/checkout", form)), buyer)
	if len(a.store.Orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(a.store.Orders))
	}
	for _, o := range a.store.Orders {
		if o.ShipAddress != "12 Marina Rd" || o.ShipState != "Lagos" || o.Status != "paid" {
			t.Fatalf("order missing delivery snapshot or not paid: %+v", o)
		}
	}
	if buyer.ShipCity != "Ikeja" {
		t.Fatal("address should be saved to the buyer's profile")
	}
	if a.store.Listings["L1"].Stock != 3 {
		t.Fatalf("stock should drop by 2 once paid, got %d", a.store.Listings["L1"].Stock)
	}
}

func TestShipAndReceive(t *testing.T) {
	a := newTestApp(t)
	s1 := addUser(a, "S1", "Seller One", "seller")
	s2 := addUser(a, "S2", "Seller Two", "seller")
	b1 := addUser(a, "B1", "Buyer", "buyer")
	b2 := addUser(a, "B2", "Other buyer", "buyer")
	o := &Order{ID: "O1", BuyerID: "B1", SellerID: "S1", Status: "paid", CreatedAt: time.Now()}
	a.store.Orders["O1"] = o

	ship := func(seller *User) {
		a.handleSellerShip(httptest.NewRecorder(), formReq("POST", "/", url.Values{"id": {"O1"}, "note": {"GIG waybill 123"}}), seller)
	}
	receive := func(buyer *User) {
		a.handleOrderReceived(httptest.NewRecorder(), formReq("POST", "/", url.Values{"id": {"O1"}}), buyer)
	}

	receive(b1) // not shipped yet
	if o.Status != "paid" {
		t.Fatal("buyer must not confirm delivery before it ships")
	}
	ship(s2) // someone else's order
	if o.Status != "paid" {
		t.Fatal("a different seller must not ship this order")
	}
	ship(s1)
	if o.Status != "shipped" || o.TrackingNote != "GIG waybill 123" || o.ShippedAt.IsZero() {
		t.Fatalf("expected shipped with note, got %+v", o)
	}
	receive(b2) // not the buyer
	if o.Status != "shipped" {
		t.Fatal("only the buyer can confirm delivery")
	}
	receive(b1)
	if o.Status != "delivered" || o.DeliveredAt.IsZero() {
		t.Fatalf("expected delivered, got %s", o.Status)
	}
}

func TestReviewRules(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "S1", "Seller", "seller")
	b := addUser(a, "B1", "Buyer", "buyer")
	other := addUser(a, "B2", "Other", "buyer")
	a.store.Orders["O1"] = &Order{ID: "O1", BuyerID: "B1", SellerID: "S1", Status: "shipped"}

	review := func(u *User, rating string) {
		a.handleReviewCreate(httptest.NewRecorder(), formReq("POST", "/", url.Values{"order_id": {"O1"}, "rating": {rating}, "comment": {"ok"}}), u)
	}
	review(b, "5")
	if len(a.store.Reviews) != 0 {
		t.Fatal("no review before delivery")
	}
	a.store.Orders["O1"].Status = "delivered"
	review(other, "5")
	review(b, "9")
	review(b, "0")
	if len(a.store.Reviews) != 0 {
		t.Fatal("a stranger or an out-of-range rating must be refused")
	}
	review(b, "4")
	review(b, "5") // second review for the same order
	if len(a.store.Reviews) != 1 {
		t.Fatalf("expected exactly one review, got %d", len(a.store.Reviews))
	}
	if avg, n := a.sellerRating("S1"); n != 1 || avg != 4 {
		t.Fatalf("rating = %v from %d", avg, n)
	}
}

func TestAdminKYCServesOnlyRealImages(t *testing.T) {
	a := newTestApp(t)
	admin := addUser(a, "A1", "Admin", "admin")
	kyc := dataPath("uploads", "kyc")
	os.MkdirAll(kyc, 0700)
	os.WriteFile(filepath.Join(kyc, "id.png"), tinyPNG, 0600)
	os.WriteFile(filepath.Join(kyc, "fake.png"), []byte("<script>alert(1)</script>"), 0600)
	a.store.Applications["ok"] = &SellerApplication{ID: "ok", NINDocPath: "data/uploads/kyc/id.png"}
	a.store.Applications["fake"] = &SellerApplication{ID: "fake", NINDocPath: "fake.png"}
	a.store.Applications["trick"] = &SellerApplication{ID: "trick", NINDocPath: "../../../../etc/passwd"}

	get := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.handleAdminKYC(rec, httptest.NewRequest("GET", "/admin/kyc?id="+id, nil), admin)
		return rec
	}
	if rec := get("ok"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("expected the image, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if get("fake").Code != 404 {
		t.Fatal("a file that isn't really an image must not be served")
	}
	if get("trick").Code != 404 {
		t.Fatal("path tricks must not escape the KYC folder")
	}
	if get("missing").Code != 404 {
		t.Fatal("unknown application must 404")
	}
}

func multipartReq(t *testing.T, fields map[string]string, files map[string][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for name, data := range files { // name is the uploaded file's name
		fw, _ := mw.CreateFormFile("images", name)
		fw.Write(data)
	}
	mw.Close()
	r := httptest.NewRequest("POST", "/seller/listings/edit", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestUploadsKeepRealImageTypeOnly(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()

	// A PNG named like a web page must be stored as .png, never .html.
	r := multipartReq(t, nil, map[string][]byte{"evil.html": tinyPNG})
	r.ParseMultipartForm(1 << 20)
	paths, err := saveUploadedFiles(r, "images", dir, 6)
	if err != nil || len(paths) != 1 || filepath.Ext(paths[0]) != ".png" {
		t.Fatalf("expected one .png, got %v (err %v)", paths, err)
	}
	// Non-images are refused outright.
	r = multipartReq(t, nil, map[string][]byte{"x.png": []byte("<html>hi</html>")})
	r.ParseMultipartForm(1 << 20)
	if _, err := saveUploadedFiles(r, "images", dir, 6); err == nil {
		t.Fatal("an HTML file must be rejected")
	}
	_ = a
}

func TestEditListing(t *testing.T) {
	a := newTestApp(t)
	seller := addUser(a, "S1", "Seller", "seller")
	other := addUser(a, "S2", "Other", "seller")
	var catID string
	for id := range a.store.Categories {
		catID = id
		break
	}
	dir := dataPath("uploads", "listings")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "old.png"), tinyPNG, 0600)
	l := &Listing{ID: "L1", SellerID: "S1", Title: "old", PriceKobo: 100, Stock: 0, Status: "sold_out", CategoryID: catID,
		ImagePaths: []string{"data/uploads/listings/old.png"}}
	a.store.Listings["L1"] = l

	fields := map[string]string{"id": "L1", "title": "New title", "description": "d", "category_id": catID, "price": "19.99", "stock": "4"}
	// Someone else's listing can't be edited.
	a.handleSellerListingEditPost(httptest.NewRecorder(), multipartReq(t, fields, nil), other)
	if l.Title != "old" {
		t.Fatal("another seller edited this listing")
	}

	fields["remove_image"] = "old.png"
	req := multipartReq(t, fields, map[string][]byte{"new.png": tinyPNG})
	req.MultipartForm = nil
	a.handleSellerListingEditPost(httptest.NewRecorder(), req, seller)
	if l.Title != "New title" || l.PriceKobo != 1999 {
		t.Fatalf("19.99 must be stored as 1999 kobo (not 1998): %+v", l)
	}
	if l.Status != "active" {
		t.Fatalf("restocking should put a sold-out item back on the market, status=%s", l.Status)
	}
	if len(l.ImagePaths) != 1 || strings.Contains(l.ImagePaths[0], "old.png") {
		t.Fatalf("expected old photo replaced by the new one, got %v", l.ImagePaths)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.png")); err == nil {
		t.Fatal("removed photo should be deleted from disk")
	}

	fields["stock"] = "0"
	delete(fields, "remove_image")
	a.handleSellerListingEditPost(httptest.NewRecorder(), multipartReq(t, fields, nil), seller)
	if l.Status != "sold_out" {
		t.Fatalf("zero stock should take it off the market, status=%s", l.Status)
	}
	fields["price"] = "-5"
	fields["stock"] = "3"
	a.handleSellerListingEditPost(httptest.NewRecorder(), multipartReq(t, fields, nil), seller)
	if l.PriceKobo != 1999 {
		t.Fatal("an invalid price must not be saved")
	}
}

func TestUnreadCounts(t *testing.T) {
	a := newTestApp(t)
	buyer := addUser(a, "B1", "Buyer", "buyer")
	seller := addUser(a, "S1", "Seller", "seller")
	admin := addUser(a, "A1", "Admin", "admin")
	conv := &Conversation{ID: "C1", BuyerID: "B1", SellerID: "S1"}
	support := &Conversation{ID: "C2", BuyerID: "B1", SellerID: supportSellerID}
	a.store.Conversations["C1"], a.store.Conversations["C2"] = conv, support

	now := time.Now()
	a.store.Messages["M1"] = &Message{ID: "M1", ConversationID: "C1", SenderID: "B1", Body: "hi", CreatedAt: now}
	if a.unreadCount(buyer) != 0 || a.unreadCount(seller) != 1 {
		t.Fatalf("seller should have 1 unread, buyer 0 (got %d / %d)", a.unreadCount(seller), a.unreadCount(buyer))
	}
	a.markRead(conv, seller, false)
	if a.unreadCount(seller) != 0 {
		t.Fatal("opening the chat should clear the unread count")
	}

	a.store.Messages["M2"] = &Message{ID: "M2", ConversationID: "C2", SenderID: "B1", Body: "help", CreatedAt: now.Add(time.Second)}
	if a.unreadCount(admin) != 1 {
		t.Fatal("support messages should show as unread for admins")
	}
	if a.unreadCount(seller) != 0 {
		t.Fatal("support threads are not the seller's")
	}
}

func TestHomePagingSortingAndFilters(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "S1", "Seller", "seller")
	bad := addUser(a, "S2", "Banned", "seller")
	bad.Suspended = true
	base := time.Now()
	for i := 0; i < 30; i++ {
		id := "L" + string(rune('A'+i))
		a.store.Listings[id] = &Listing{ID: id, SellerID: "S1", Title: "item " + id, PriceKobo: (i + 1) * 10000, Stock: 1, Status: "active", CreatedAt: base.Add(time.Duration(i) * time.Minute)}
	}
	a.store.Listings["HID"] = &Listing{ID: "HID", SellerID: "S2", Title: "zzbanned-item", PriceKobo: 100, Stock: 1, Status: "active", CreatedAt: base}

	get := func(q string) string {
		rec := httptest.NewRecorder()
		a.handleHome(rec, httptest.NewRequest("GET", "/"+q, nil))
		return rec.Body.String()
	}
	cards := func(body string) int { return strings.Count(body, `class="stall-card"`) }

	p1, p3 := get(""), get("?page=3")
	if cards(p1) != 12 || cards(p3) != 6 || !strings.Contains(p1, "Page 1 of 3") {
		t.Fatalf("paging wrong: page1=%d page3=%d", cards(p1), cards(p3))
	}
	if strings.Contains(p1, "zzbanned-item") || cards(get("?q=zzbanned")) != 0 {
		t.Fatal("a suspended seller's listing must not appear")
	}
	if cards(get("?page=99")) != 6 {
		t.Fatal("an out-of-range page should show the last page")
	}
	cheap := get("?sort=price_asc")
	if strings.Index(cheap, "item LA") > strings.Index(cheap, "item LB") || strings.Index(cheap, "item LA") < 0 {
		t.Fatal("price_asc should list the cheapest first")
	}
	if n := cards(get("?min=100&max=300")); n != 3 { // ₦100, ₦200, ₦300
		t.Fatalf("price filter returned %d items, want 3", n)
	}
	if rec := httptest.NewRecorder(); true {
		a.handleHome(rec, httptest.NewRequest("GET", "/no-such-page", nil))
		if rec.Code != 404 {
			t.Fatalf("unknown paths should 404, got %d", rec.Code)
		}
	}
}

func TestChangePasswordSignsOutOtherDevices(t *testing.T) {
	a := newTestApp(t)
	u := addUser(a, "U1", "Ada", "buyer")
	rec := httptest.NewRecorder()
	a.startSession(rec, u.ID)
	mine := rec.Result().Cookies()[0]
	a.startSession(httptest.NewRecorder(), u.ID) // a second device

	post := func(cur, next, confirm string) {
		r := formReq("POST", "/account/password", url.Values{"current_password": {cur}, "new_password": {next}, "confirm_password": {confirm}})
		r.AddCookie(mine)
		a.handlePasswordChange(httptest.NewRecorder(), r, u)
	}
	post("wrong", "newpass1", "newpass1")
	if !verifyPassword("secret123", u.PasswordHash) {
		t.Fatal("password changed despite a wrong current password")
	}
	post("secret123", "newpass1", "different")
	if !verifyPassword("secret123", u.PasswordHash) {
		t.Fatal("password changed although the confirmation didn't match")
	}
	post("secret123", "newpass1", "newpass1")
	if !verifyPassword("newpass1", u.PasswordHash) {
		t.Fatal("password was not changed")
	}
	if len(a.store.Sessions) != 1 {
		t.Fatalf("expected only this device to stay signed in, %d sessions left", len(a.store.Sessions))
	}
	if _, ok := a.store.Sessions[mine.Value]; !ok {
		t.Fatal("the session that changed the password should survive")
	}
}

func TestFavoritesAndReports(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "S1", "Seller", "seller")
	u := addUser(a, "B1", "Buyer", "buyer")
	a.store.Listings["L1"] = &Listing{ID: "L1", SellerID: "S1", Title: "x", Status: "active"}

	toggle := func() {
		a.handleFavoriteToggle(httptest.NewRecorder(), formReq("POST", "/", url.Values{"listing_id": {"L1"}}), u)
	}
	toggle()
	if !a.isFavorite("B1", "L1") {
		t.Fatal("expected the listing to be saved")
	}
	toggle()
	if a.isFavorite("B1", "L1") {
		t.Fatal("expected the second tap to remove it")
	}

	rep := func(reason string) {
		a.handleReportPost(httptest.NewRecorder(), formReq("POST", "/report", url.Values{"listing_id": {"L1"}, "reason": {reason}}), u)
	}
	rep("made-up reason")
	if len(a.store.Reports) != 0 {
		t.Fatal("an unknown reason must be refused")
	}
	rep("Scam or fake item")
	rep("Scam or fake item") // repeat while the first is still open
	if len(a.store.Reports) != 1 {
		t.Fatalf("expected one open report, got %d", len(a.store.Reports))
	}
	for _, r := range a.store.Reports {
		if r.SellerID != "S1" {
			t.Fatal("a listing report should record the seller too")
		}
	}
}

func TestAdminOrdersAndCounts(t *testing.T) {
	a := newTestApp(t)
	admin := addUser(a, "A1", "Admin", "admin")
	addUser(a, "S1", "Seller", "seller")
	addUser(a, "B1", "Buyer", "buyer")
	a.store.Orders["P"] = &Order{ID: "P", BuyerID: "B1", SellerID: "S1", Status: "delivered", TotalKobo: 10000, PlatformFeeKobo: 500, SellerPayoutKobo: 9500, CreatedAt: time.Now()}
	a.store.Orders["F"] = &Order{ID: "F", BuyerID: "B1", SellerID: "S1", Status: "payment_failed", TotalKobo: 99999, CreatedAt: time.Now()}
	a.store.Orders["X"] = &Order{ID: "X", BuyerID: "B1", SellerID: "S1", Status: "paid", TotalKobo: 5000, OversoldWarning: "ran short", CreatedAt: time.Now()}
	a.store.Reports["R"] = &Report{ID: "R", Status: "open"}

	if c := a.adminCounts(); c.NeedsAttention != 1 || c.OpenReports != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
	rec := httptest.NewRecorder()
	a.handleAdminOrders(rec, httptest.NewRequest("GET", "/admin/orders", nil), admin)
	body := rec.Body.String()
	if !strings.Contains(body, "ran short") || !strings.Contains(body, "₦150") { // gross = 10000+5000 kobo
		t.Fatal("expected the oversold warning and the paid-only sales total (₦150)")
	}
	if strings.Contains(body, "₦1,149") { // would mean the failed order was counted
		t.Fatal("failed orders must not count as sales")
	}
}

func TestAdminDashboardFlagsDefaultPassword(t *testing.T) {
	a := newTestApp(t)
	admin := addUser(a, "A1", "Admin", "admin")
	admin.PasswordHash = hashPassword("changeme123")
	if !a.hasDefaultPassword(admin) {
		t.Fatal("expected the default password to be detected")
	}
	admin.PasswordHash = hashPassword("something-else-entirely")
	if a.hasDefaultPassword(admin) {
		t.Fatal("a changed password must not be flagged")
	}
}

func TestSessionCookieSecureOnHTTPS(t *testing.T) {
	a := newTestApp(t)
	a.payments.BaseURL = "https://kaya.example.com"
	rec := httptest.NewRecorder()
	a.startSession(rec, "U1")
	if !rec.Result().Cookies()[0].Secure {
		t.Fatal("the session cookie should be Secure on an https site")
	}
	a.payments.BaseURL = ""
	rec = httptest.NewRecorder()
	a.startSession(rec, "U1")
	if rec.Result().Cookies()[0].Secure {
		t.Fatal("a Secure cookie would break plain-http local development")
	}
}
