package main

import (
	"testing"
	"time"
)

func TestApplyVerifiedTransaction_RecoversFromWronglyFailedOrder(t *testing.T) {
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

	// Simulate the bug: order gets wrongly marked failed (like the old
	// premature-callback logic did for async bank transfer), which restores
	// stock from 4 (already reserved at checkout) back to 5.
	listing.Stock = 4 // pretend checkout already reserved 1 unit
	app.markOrderFailed("ORDER1")
	if order.Status != "payment_failed" {
		t.Fatalf("expected payment_failed, got %s", order.Status)
	}
	if listing.Stock != 5 {
		t.Fatalf("expected stock restored to 5, got %d", listing.Stock)
	}

	// Now the real confirmation arrives late (e.g. via manual recheck) -
	// verify it flips to paid AND re-reserves the stock so it isn't
	// double-available.
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
		t.Fatalf("expected stock re-reserved to 4, got %d", listing.Stock)
	}

	// Idempotency: applying the same verification again must not
	// double-decrement stock.
	if err := app.applyVerifiedTransaction(verify); err != nil {
		t.Fatalf("second apply failed: %v", err)
	}
	if listing.Stock != 4 {
		t.Fatalf("expected stock still 4 after duplicate confirmation, got %d", listing.Stock)
	}
}
