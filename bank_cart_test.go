package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func seedBanks(t *testing.T, banks []Bank) {
	t.Helper()
	bankCacheMu.Lock()
	bankCache, bankCacheAt = banks, time.Now()
	bankCacheMu.Unlock()
	t.Cleanup(func() {
		bankCacheMu.Lock()
		bankCache = nil
		bankCacheMu.Unlock()
	})
}

var sampleBanks = []Bank{
	{Code: "044", Name: "Access Bank"},
	{Code: "058", Name: "Guaranty Trust Bank"},
	{Code: "057", Name: "Zenith Bank"},
	{Code: "999", Name: "OPay Digital Services Limited (OPay)"},
}

func TestSortBanksIgnoresCase(t *testing.T) {
	banks := []Bank{{Name: "zenith"}, {Name: "Access"}, {Name: "9 Payment"}, {Name: "ALAT"}, {Name: "opay"}}
	sortBanks(banks)
	var got []string
	for _, b := range banks {
		got = append(got, b.Name)
	}
	want := "9 Payment,Access,ALAT,opay,zenith"
	if strings.Join(got, ",") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestPayoutPageHasBankSearch(t *testing.T) {
	a := newTestApp(t)
	a.payments = PaymentConfig{SecretKey: "x", BaseURL: "http://x"}
	seedBanks(t, sampleBanks)
	seller := addUser(a, "S1", "Seller", "seller")

	rec := httptest.NewRecorder()
	a.handleSellerPayoutGet(rec, httptest.NewRequest("GET", "/seller/payout", nil), seller)
	body := rec.Body.String()
	for _, want := range []string{`id="bank_search"`, "Guaranty Trust Bank", "Zenith Bank", `name="bank_code"`, `pattern="[0-9]{10}"`} {
		if !strings.Contains(body, want) {
			t.Errorf("payout page is missing %q", want)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Fatal("the page script was blocked by the template escaper")
	}
}

func TestPayoutKeepsBankListAndChoicesAfterAMistake(t *testing.T) {
	a := newTestApp(t)
	a.payments = PaymentConfig{SecretKey: "x", BaseURL: "http://x"}
	seedBanks(t, sampleBanks)
	seller := addUser(a, "S1", "Seller", "seller")

	post := func(code, acct string) string {
		rec := httptest.NewRecorder()
		a.handleSellerPayoutPost(rec, formReq("POST", "/seller/payout", url.Values{"bank_code": {code}, "bank_name": {"Zenith Bank"}, "account_number": {acct}}), seller)
		return rec.Body.String()
	}

	body := post("057", "12345") // too short: must be refused before Flutterwave is contacted
	if !strings.Contains(body, "exactly 10 digits") {
		t.Fatal("expected the 10-digit message")
	}
	if !strings.Contains(body, `id="bank_search"`) || !strings.Contains(body, "Guaranty Trust Bank") {
		t.Fatal("the bank list and search box must still be there after a mistake")
	}
	if !strings.Contains(body, `value="057" selected`) {
		t.Fatal("the bank the seller chose must stay selected")
	}
	if !strings.Contains(body, `value="12345"`) {
		t.Fatal("the account number they typed should be kept")
	}
	if seller.BankAccountNumber != "" || seller.FlutterwaveSubaccountID != "" {
		t.Fatal("nothing should be saved for an invalid account number")
	}
	for _, bad := range []string{"12345678a0", "1234 5678", "12345678901"} {
		if body := post("057", bad); !strings.Contains(body, "exactly 10 digits") && bad != "1234 5678" {
			t.Errorf("%q should be refused", bad)
		}
	}
	if body := post("", "0123456789"); !strings.Contains(body, "Choose your bank") {
		t.Fatal("a missing bank must be reported")
	}
}

func TestCartAndCheckoutShowItemPictures(t *testing.T) {
	a := newTestApp(t)
	addUser(a, "S1", "Seller", "seller")
	buyer := addUser(a, "B1", "Buyer", "buyer")
	a.store.Listings["L1"] = &Listing{ID: "L1", SellerID: "S1", Title: "Ankara Fabric", PriceKobo: 100000, Stock: 5, Status: "active",
		ImagePaths: []string{"data/uploads/listings/front.png", "data/uploads/listings/back.png"}}
	a.store.Listings["L2"] = &Listing{ID: "L2", SellerID: "S1", Title: "No Photo Item", PriceKobo: 5000, Stock: 5, Status: "active"}

	cartRec := httptest.NewRecorder()
	writeCart(cartRec, cart{"L1": 1, "L2": 1})
	cookie := cartRec.Result().Cookies()[0]

	req := httptest.NewRequest("GET", "/cart", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.handleCartView(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `src="/uploads/listings/front.png"`) {
		t.Fatal("the cart should show the item's first photo")
	}
	if strings.Contains(body, "back.png") {
		t.Fatal("only the first photo belongs in the cart")
	}
	if strings.Count(body, "thumb-fallback")-strings.Count(body, ".thumb-fallback") != 1 {
		t.Fatal("an item without photos should get a placeholder, once")
	}
	if !strings.Contains(body, `href="/listing?id=L1"`) {
		t.Fatal("the item should link back to its page")
	}

	req = httptest.NewRequest("GET", "/checkout", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	a.handleCheckoutGet(rec, req, buyer)
	if !strings.Contains(rec.Body.String(), `src="/uploads/listings/front.png"`) {
		t.Fatal("the checkout summary should show the photo too")
	}

	rec = httptest.NewRecorder()
	a.renderBuyNowPage(rec, httptest.NewRequest("GET", "/buy-now?listing_id=L1&qty=1", nil), buyer, shippingInfo{}, "")
	if !strings.Contains(rec.Body.String(), `src="/uploads/listings/front.png"`) {
		t.Fatal("buy-now summary should show the photo")
	}
}
