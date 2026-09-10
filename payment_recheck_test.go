package main

import (
	"testing"
	"time"
)

// TestStockUntouchedUntilPaid verifies the core rule behind feature #4:
// a listing's stock must NOT change when an order is created (checkout or
// buy-now) or when it fails - only when a payment is actually confirmed.
// This is what keeps an item visible and purchasable by other buyers right
// up until someone actually pays for it.
func TestStockUntouchedUntilPaid(t *testing.T) {
	app := &App{store: NewStore(), payments: PaymentConfig{SecretKey: "x", BaseURL: "http://x"}}

	listing := &Listing{ID: "L1", SellerID: "S1", Title: "phone", PriceKobo: 1000, Stock: 5, Status: "active"}
	app.store.Listings["L1"] = listing
	app.store.Users["S1"] = &User{ID: "S1", Name: "Seller", Email: "s@x.com"}
	app.store.Users["B1"] = &User{ID: "B1", Name: "Buyer", Email: "b@x.com"}

	order := &Order{
		ID:        "ORDER1",
		BuyerID:   "B1",
		SellerID:  "S1",
		TotalKobo: 1000,
		Status:    "pending_payment",
		Items:     []OrderItem{{ListingID: "L1", SellerID: "S1", Title: "phone", Quantity: 1, PriceKobo: 1000}},
		CreatedAt: time.Now(),
	}
	app.store.Orders["ORDER1"] = order

	// Order exists and is pending - stock must be completely untouched.
	if listing.Stock != 5 {
		t.Fatalf("expected stock still 5 while pending, got %d", listing.Stock)
	}

	// A failed payment must not touch stock either - it was never reserved.
	app.markOrderFailed("ORDER1")
	if order.Status != "payment_failed" {
		t.Fatalf("expected payment_failed, got %s", order.Status)
	}
	if listing.Stock != 5 {
		t.Fatalf("expected stock still 5 after failure, got %d", listing.Stock)
	}

	// Now a real confirmation arrives (e.g. via manual recheck after a
	// wrongly-failed async payment) - THIS is the only place stock should
	// change, decrementing by exactly the ordered quantity.
	verify := &flwVerifyResponse{Status: "success"}
	verify.Data.TxRef = "ORDER1"
	verify.Data.FlwRef = "FLW-REF-1"
	verify.Data.Amount = 10.0 // 1000 kobo = 10 naira
	verify.Data.Currency = "NGN"
	verify.Data.Status = "successful"

	if err := app.applyVerifiedTransaction(verify); err != nil {
		t.Fatalf("applyVerifiedTransaction failed: %v", err)
	}
	if order.Status != "paid" {
		t.Fatalf("expected paid, got %s", order.Status)
	}
	if listing.Stock != 4 {
		t.Fatalf("expected stock decremented to 4, got %d", listing.Stock)
	}
	if order.OversoldWarning != "" {
		t.Fatalf("expected no oversold warning, got %q", order.OversoldWarning)
	}

	// Idempotency: applying the same verification twice must not
	// double-decrement stock.
	if err := app.applyVerifiedTransaction(verify); err != nil {
		t.Fatalf("second apply failed: %v", err)
	}
	if listing.Stock != 4 {
		t.Fatalf("expected stock still 4 after duplicate confirmation, got %d", listing.Stock)
	}
}

// TestOversoldProtection verifies that if stock runs out between order
// creation and confirmed payment (two buyers both completing payment for
// the last unit - the real tradeoff of not reserving stock upfront), the
// order still gets marked paid (money was taken, no refund API wired in),
// stock floors at zero instead of going negative, and the order is clearly
// flagged for manual admin follow-up.
func TestOversoldProtection(t *testing.T) {
	app := &App{store: NewStore(), payments: PaymentConfig{SecretKey: "x", BaseURL: "http://x"}}

	listing := &Listing{ID: "L1", SellerID: "S1", Title: "last phone", PriceKobo: 1000, Stock: 0, Status: "sold_out"}
	app.store.Listings["L1"] = listing
	app.store.Users["S1"] = &User{ID: "S1", Name: "Seller", Email: "s@x.com"}
	app.store.Users["B1"] = &User{ID: "B1", Name: "Buyer", Email: "b@x.com"}

	order := &Order{
		ID:        "ORDER2",
		BuyerID:   "B1",
		SellerID:  "S1",
		TotalKobo: 1000,
		Status:    "pending_payment",
		Items:     []OrderItem{{ListingID: "L1", SellerID: "S1", Title: "last phone", Quantity: 1, PriceKobo: 1000}},
		CreatedAt: time.Now(),
	}
	app.store.Orders["ORDER2"] = order

	verify := &flwVerifyResponse{Status: "success"}
	verify.Data.TxRef = "ORDER2"
	verify.Data.FlwRef = "FLW-REF-2"
	verify.Data.Amount = 10.0
	verify.Data.Currency = "NGN"
	verify.Data.Status = "successful"

	if err := app.applyVerifiedTransaction(verify); err != nil {
		t.Fatalf("applyVerifiedTransaction failed: %v", err)
	}
	if order.Status != "paid" {
		t.Fatalf("expected paid even when oversold (money was taken), got %s", order.Status)
	}
	if listing.Stock != 0 {
		t.Fatalf("expected stock floored at 0, got %d", listing.Stock)
	}
	if order.OversoldWarning == "" {
		t.Fatalf("expected an oversold warning to be set for admin follow-up")
	}
}
