package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// PaymentConfig holds everything needed to talk to Flutterwave. Populated
// from environment variables in main.go - never hardcode these.
type PaymentConfig struct {
	SecretKey  string // FLW_SECRET_KEY - authorizes API calls
	SecretHash string // FLW_SECRET_HASH - set the same value in your Flutterwave dashboard's webhook settings
	BaseURL    string // e.g. https://kaya.onrender.com - used to build the redirect URL
}

func loadPaymentConfig() PaymentConfig {
	return PaymentConfig{
		SecretKey:  os.Getenv("FLW_SECRET_KEY"),
		SecretHash: os.Getenv("FLW_SECRET_HASH"),
		BaseURL:    os.Getenv("APP_BASE_URL"),
	}
}

// Configured reports whether real Flutterwave keys are present. When false,
// checkout falls back to a dev-mode "mark as paid immediately" path so the
// rest of the app is still testable without live keys.
func (p PaymentConfig) Configured() bool {
	return p.SecretKey != "" && p.BaseURL != ""
}

type flwInitRequest struct {
	TxRef          string             `json:"tx_ref"`
	Amount         string             `json:"amount"` // Flutterwave wants a string here
	Currency       string             `json:"currency"`
	RedirectURL    string             `json:"redirect_url"`
	Customer       flwCustomer        `json:"customer"`
	Customizations flwCustomizations  `json:"customizations"`
	Subaccounts    []flwSubaccountRef `json:"subaccounts,omitempty"`
}

// flwSubaccountRef routes this transaction's split to a specific seller.
// We intentionally omit any per-transaction override fields here (like
// transaction_charge_type) - Flutterwave's docs are ambiguous about how
// those interact with the subaccount's own split_type/split_value, and this
// is real money. Instead every order is single-seller (see checkout
// grouping in order.go), so the subaccount's own default split - set once
// at creation time in createFlutterwaveSubaccount - is all that's needed.
type flwSubaccountRef struct {
	ID string `json:"id"`
}

type flwCustomer struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type flwCustomizations struct {
	Title string `json:"title"`
}

type flwInitResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Link string `json:"link"`
	} `json:"data"`
}

type flwVerifyResponse struct {
	Status string `json:"status"`
	Data   struct {
		ID       int64   `json:"id"`
		TxRef    string  `json:"tx_ref"`
		FlwRef   string  `json:"flw_ref"`
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
		Status   string  `json:"status"` // "successful", "failed", etc.
	} `json:"data"`
}

// initiateFlutterwavePayment asks Flutterwave for a hosted payment link for
// this order and returns the URL to redirect the buyer to. Every order
// belongs to exactly one seller (see checkout grouping in order.go), so if
// that seller has a Flutterwave subaccount, this transaction is routed
// through it - Flutterwave automatically settles the seller's share to
// their bank account and keeps your platform commission, based on the
// split_value set when the subaccount was created.
func (a *App) initiateFlutterwavePayment(order *Order, buyer *User) (string, error) {
	amountNaira := float64(order.TotalKobo) / 100.0

	payload := flwInitRequest{
		TxRef:       order.ID, // must be unique per payment attempt - order.ID is unique per order
		Amount:      fmt.Sprintf("%.2f", amountNaira),
		Currency:    "NGN",
		RedirectURL: a.payments.BaseURL + "/payment/callback",
		Customer:    flwCustomer{Email: buyer.Email, Name: buyer.Name},
		Customizations: flwCustomizations{
			Title: "Kaya Marketplace",
		},
	}

	a.store.mu.RLock()
	seller, ok := a.store.Users[order.SellerID]
	a.store.mu.RUnlock()
	if ok && seller.FlutterwaveSubaccountID != "" {
		payload.Subaccounts = []flwSubaccountRef{{ID: seller.FlutterwaveSubaccountID}}
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "https://api.flutterwave.com/v3/payments", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var parsed flwInitResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("could not parse Flutterwave response: %w", err)
	}
	if parsed.Status != "success" || parsed.Data.Link == "" {
		return "", fmt.Errorf("Flutterwave declined to initiate payment: %s", parsed.Message)
	}
	return parsed.Data.Link, nil
}

// verifyFlutterwaveTransaction asks Flutterwave directly whether a
// transaction actually succeeded. Never trust a redirect or webhook body
// alone - always confirm against this endpoint before crediting an order.
func (a *App) verifyFlutterwaveTransaction(transactionID string) (*flwVerifyResponse, error) {
	url := fmt.Sprintf("https://api.flutterwave.com/v3/transactions/%s/verify", transactionID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var parsed flwVerifyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not parse verify response: %w", err)
	}
	return &parsed, nil
}

// verifyFlutterwaveByReference is the same check as
// verifyFlutterwaveTransaction, but looked up by our own order ID (tx_ref)
// instead of Flutterwave's numeric transaction ID. This is more robust for
// re-checking a specific order later - e.g. from the admin "Recheck
// payment" action - since we always know our own order ID, whereas the
// numeric transaction_id depends on a query parameter that isn't always
// present or reliable (notably for async methods like bank transfer).
func (a *App) verifyFlutterwaveByReference(txRef string) (*flwVerifyResponse, error) {
	url := "https://api.flutterwave.com/v3/transactions/verify_by_reference?tx_ref=" + txRef
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var parsed flwVerifyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not parse verify_by_reference response: %w", err)
	}
	return &parsed, nil
}

// applyVerifiedTransaction takes an already-fetched Flutterwave verification
// result and, only if amount/currency/status all check out, marks the
// matching order paid. Shared by every path that confirms a payment
// (redirect callback, webhook, and the admin manual recheck) so they can
// never disagree about what counts as "paid".
func (a *App) applyVerifiedTransaction(verify *flwVerifyResponse) error {
	if verify.Status != "success" || verify.Data.Status != "successful" {
		return fmt.Errorf("transaction not successful (status: %s)", verify.Data.Status)
	}

	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	order, ok := a.store.Orders[verify.Data.TxRef]
	if !ok {
		return fmt.Errorf("no matching order for tx_ref %s", verify.Data.TxRef)
	}
	if order.Status == "paid" {
		return nil // already confirmed - e.g. webhook arrived after the redirect already handled it
	}

	expectedNaira := float64(order.TotalKobo) / 100.0
	if verify.Data.Currency != "NGN" || verify.Data.Amount < expectedNaira {
		return fmt.Errorf("amount/currency mismatch: expected %.2f NGN, got %.2f %s", expectedNaira, verify.Data.Amount, verify.Data.Currency)
	}

	wasFailed := order.Status == "payment_failed"
	order.Status = "paid"
	order.PaymentRef = verify.Data.FlwRef
	// If this order had been wrongly marked failed earlier (e.g. by the
	// pending/async misclassification this function's callers now avoid),
	// its reserved stock was already released back to the listing. Since
	// the payment did in fact succeed, take that stock reservation back out
	// again so it isn't double-available.
	if wasFailed {
		for _, item := range order.Items {
			if l, ok := a.store.Listings[item.ListingID]; ok {
				l.Stock -= item.Quantity
				if l.Stock <= 0 {
					l.Stock = 0
					l.Status = "sold_out"
				}
			}
		}
	}
	a.store.save()

	buyer := a.store.Users[order.BuyerID]
	seller := a.store.Users[order.SellerID]
	notifyOrderPaid(order, buyer, seller)
	return nil
}

// confirmOrderPaid re-verifies a transaction (by Flutterwave's numeric
// transaction ID) against Flutterwave and applies the result. Called from
// the redirect callback and the webhook, so it's written to be safe to call
// twice for the same order (idempotent).
func (a *App) confirmOrderPaid(transactionID string) error {
	verify, err := a.verifyFlutterwaveTransaction(transactionID)
	if err != nil {
		return err
	}
	return a.applyVerifiedTransaction(verify)
}

// confirmOrderPaidByRef is the same idea, looked up by tx_ref (our own
// order ID) instead - used for the admin manual recheck.
func (a *App) confirmOrderPaidByRef(txRef string) error {
	verify, err := a.verifyFlutterwaveByReference(txRef)
	if err != nil {
		return err
	}
	return a.applyVerifiedTransaction(verify)
}

// notifyOrderPaid is a stand-in for real notification delivery (email/SMS).
// Replace with an actual provider call - same pattern as sendResetEmail in
// password_reset.go. For now it logs clearly so you can see the trigger
// point is correct while wiring in a real notifier.
func notifyOrderPaid(order *Order, buyer, seller *User) {
	buyerEmail, sellerEmail := "unknown", "unknown"
	if buyer != nil {
		buyerEmail = buyer.Email
	}
	if seller != nil {
		sellerEmail = seller.Email
	}
	log.Printf("[order paid] order %s: would notify buyer %s (order confirmed) and seller %s (payout of %s en route to their bank)",
		order.ID, buyerEmail, sellerEmail, formatNaira(order.SellerPayoutKobo))
}

// markOrderFailed restores stock for an order whose payment did not go
// through, so items aren't stuck reserved forever.
func (a *App) markOrderFailed(orderID string) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	order, ok := a.store.Orders[orderID]
	if !ok || order.Status == "paid" || order.Status == "payment_failed" {
		return
	}
	order.Status = "payment_failed"
	for _, item := range order.Items {
		if l, ok := a.store.Listings[item.ListingID]; ok {
			l.Stock += item.Quantity
			if l.Status == "sold_out" && l.Stock > 0 {
				l.Status = "active"
			}
		}
	}
	a.store.save()
}

// isTerminalFailure reports whether a Flutterwave status string represents
// a payment that definitively did NOT succeed and will never become
// successful later - as opposed to "pending", which async methods like
// bank transfer and USSD legitimately sit in for a while before either
// succeeding or failing. Only terminal failures should release reserved
// stock; anything else (including statuses we don't recognize) should be
// left alone and reconciled later via the webhook or a manual recheck.
func isTerminalFailure(status string) bool {
	switch status {
	case "cancelled", "failed":
		return true
	default:
		return false
	}
}

// handlePaymentCallback is where Flutterwave redirects the buyer's browser
// back to after they finish on the hosted checkout page. This is a
// convenience for the user - the webhook below is the source of truth, and
// for async methods (bank transfer, USSD) this redirect often fires before
// the payment has actually been confirmed, so a non-"successful" status
// here does NOT necessarily mean it failed.
func (a *App) handlePaymentCallback(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	txID := r.URL.Query().Get("transaction_id")
	txRef := r.URL.Query().Get("tx_ref")

	switch {
	case status == "successful" && txID != "":
		if err := a.confirmOrderPaid(txID); err != nil {
			log.Printf("payment callback: verify failed for tx %s: %v", txID, err)
		}
	case isTerminalFailure(status) && txRef != "":
		a.markOrderFailed(txRef)
	default:
		// Likely "pending" (common for bank transfer/USSD) or an unknown
		// status - leave the order as pending_payment. The webhook, or an
		// admin recheck, will resolve it once the real outcome is known.
		log.Printf("payment callback: status %q for tx_ref %s is not a confirmed outcome yet - leaving order pending", status, txRef)
	}

	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

// handlePaymentWebhook is the server-to-server confirmation Flutterwave
// sends regardless of whether the buyer's browser makes it back to your
// site. This is the reliable path - configure this URL in your Flutterwave
// dashboard under Settings > Webhooks, with the same secret hash as
// FLW_SECRET_HASH. Same caution as the callback above: only a terminal
// failure status should release stock - "pending" must be left alone.
func (a *App) handlePaymentWebhook(w http.ResponseWriter, r *http.Request) {
	signature := r.Header.Get("verif-hash")
	if a.payments.SecretHash == "" || subtle.ConstantTimeCompare([]byte(signature), []byte(a.payments.SecretHash)) != 1 {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var payload struct {
		Event string `json:"event"`
		Data  struct {
			ID     int64  `json:"id"`
			TxRef  string `json:"tx_ref"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	switch {
	case payload.Data.Status == "successful":
		if err := a.confirmOrderPaid(fmt.Sprintf("%d", payload.Data.ID)); err != nil {
			log.Printf("webhook: verify failed for tx %d: %v", payload.Data.ID, err)
		}
	case isTerminalFailure(payload.Data.Status) && payload.Data.TxRef != "":
		a.markOrderFailed(payload.Data.TxRef)
	default:
		log.Printf("webhook: status %q for tx_ref %s is not a confirmed outcome yet - leaving order pending", payload.Data.Status, payload.Data.TxRef)
	}

	w.WriteHeader(http.StatusOK)
}
