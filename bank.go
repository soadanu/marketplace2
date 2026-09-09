package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Bank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type flwBankListResponse struct {
	Status string `json:"status"`
	Data   []Bank `json:"data"`
}

// A short-lived cache for the bank list - it barely ever changes, and
// caching means a slow or flaky Flutterwave response only affects the first
// seller to load the page each hour, not every single page view.
var (
	bankCacheMu      sync.Mutex
	bankCache        []Bank
	bankCacheAt      time.Time
	bankCacheTimeout = 5 * time.Second // don't let a slow Flutterwave response hang the page load
)

// fetchNigerianBanks asks Flutterwave for the current list of banks and
// their codes, used to populate the dropdown on the seller payout form.
// Failure here is never fatal to the page - see handleSellerPayoutGet, which
// falls back to manual bank-code entry - but the real error is always
// logged server-side so it can actually be diagnosed instead of just
// showing a generic message to the seller.
func (a *App) fetchNigerianBanks() ([]Bank, error) {
	bankCacheMu.Lock()
	if len(bankCache) > 0 && time.Since(bankCacheAt) < time.Hour {
		cached := bankCache
		bankCacheMu.Unlock()
		return cached, nil
	}
	bankCacheMu.Unlock()

	client := &http.Client{Timeout: bankCacheTimeout}
	req, err := http.NewRequest(http.MethodGet, "https://api.flutterwave.com/v3/banks/NG", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("fetchNigerianBanks: request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("fetchNigerianBanks: Flutterwave returned HTTP %d: %s", resp.StatusCode, truncateForLog(body))
		return nil, fmt.Errorf("Flutterwave returned HTTP %d", resp.StatusCode)
	}

	var parsed flwBankListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("fetchNigerianBanks: could not parse response: %v; body: %s", err, truncateForLog(body))
		return nil, fmt.Errorf("could not parse bank list: %w", err)
	}
	if parsed.Status != "success" {
		log.Printf("fetchNigerianBanks: Flutterwave status was %q, not success; body: %s", parsed.Status, truncateForLog(body))
		return nil, fmt.Errorf("Flutterwave did not return a bank list")
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Name < parsed.Data[j].Name })

	bankCacheMu.Lock()
	bankCache = parsed.Data
	bankCacheAt = time.Now()
	bankCacheMu.Unlock()

	return parsed.Data, nil
}

func truncateForLog(b []byte) string {
	const max = 300
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

type flwSubaccountRequest struct {
	AccountBank    string  `json:"account_bank"`
	AccountNumber  string  `json:"account_number"`
	BusinessName   string  `json:"business_name"`
	BusinessEmail  string  `json:"business_email"`
	BusinessMobile string  `json:"business_mobile"`
	Country        string  `json:"country"`
	SplitType      string  `json:"split_type"`
	SplitValue     float64 `json:"split_value"`
}

type flwSubaccountResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		ID            int64  `json:"id"`
		SubaccountID  string `json:"subaccount_id"`
		AccountNumber string `json:"account_number"`
		AccountName   string `json:"account_number_name"` // account name Flutterwave resolved from the bank
	} `json:"data"`
}

// findExistingSubaccount looks up a previously-created subaccount by
// account number when Flutterwave refuses to create a duplicate. This
// happens when a subaccount was successfully created on an earlier attempt
// but our own record of its ID was lost (e.g. before DATA_DIR was
// configured, a Render restart wiped the local database even though the
// subaccount still exists on Flutterwave's side). Rather than leaving the
// seller stuck, we recover the existing ID and reuse it.
func (a *App) findExistingSubaccount(accountNumber string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.flutterwave.com/v3/subaccounts", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Status string `json:"status"`
		Data   []struct {
			SubaccountID  string `json:"subaccount_id"`
			AccountNumber string `json:"account_number"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("could not parse subaccount list: %w", err)
	}
	if parsed.Status != "success" {
		return "", fmt.Errorf("Flutterwave did not return a subaccount list")
	}

	for _, s := range parsed.Data {
		if s.AccountNumber == accountNumber && s.SubaccountID != "" {
			return s.SubaccountID, nil
		}
	}
	return "", fmt.Errorf("no existing subaccount found matching account number %s", accountNumber)
}

// createFlutterwaveSubaccount registers a seller's payout account with
// Flutterwave. split_value is set to the platform's commission rate here -
// per Flutterwave's docs, split_value on a subaccount is "the amount you
// want to get as commission on each transaction", so this is Kaya's cut;
// the seller is automatically settled the remainder on every sale routed
// to this subaccount.
func (a *App) createFlutterwaveSubaccount(seller *User) (*flwSubaccountResponse, error) {
	payload := flwSubaccountRequest{
		AccountBank:    seller.BankCode,
		AccountNumber:  seller.BankAccountNumber,
		BusinessName:   seller.Name,
		BusinessEmail:  seller.Email,
		BusinessMobile: seller.Phone,
		Country:        "NG",
		SplitType:      "percentage",
		SplitValue:     platformCommissionRate(),
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "https://api.flutterwave.com/v3/subaccounts", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.payments.SecretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var parsed flwSubaccountResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("could not parse subaccount response: %w", err)
	}
	if parsed.Status != "success" {
		if strings.Contains(strings.ToLower(parsed.Message), "already exists") {
			return nil, fmt.Errorf("ALREADY_EXISTS: %s", parsed.Message)
		}
		return nil, fmt.Errorf("Flutterwave declined to create the subaccount: %s", parsed.Message)
	}
	return &parsed, nil
}

// --- HTTP handlers ---

func (a *App) handleSellerPayoutGet(w http.ResponseWriter, r *http.Request, seller *User) {
	var banks []Bank
	var bankErr string
	if a.payments.Configured() {
		var err error
		banks, err = a.fetchNigerianBanks()
		if err != nil {
			bankErr = "Could not load the bank list right now - you can still enter your bank's code manually if you know it."
		}
	}

	render(w, "seller_payout.html", map[string]any{
		"User":          seller,
		"Banks":         banks,
		"BankListError": bankErr,
		"PaymentsLive":  a.payments.Configured(),
	})
}

func (a *App) handleSellerPayoutPost(w http.ResponseWriter, r *http.Request, seller *User) {
	if !a.payments.Configured() {
		http.Error(w, "payments are not configured on this server yet (dev mode) - payout setup is disabled until FLW_SECRET_KEY and APP_BASE_URL are set", http.StatusServiceUnavailable)
		return
	}

	bankCode := r.FormValue("bank_code")
	bankName := r.FormValue("bank_name")
	accountNumber := r.FormValue("account_number")
	if bankCode == "" || accountNumber == "" {
		render(w, "seller_payout.html", map[string]any{"User": seller, "Error": "Bank and account number are required.", "PaymentsLive": true})
		return
	}

	a.store.mu.Lock()
	seller.BankCode = bankCode
	seller.BankName = bankName
	seller.BankAccountNumber = accountNumber
	a.store.save()
	a.store.mu.Unlock()

	result, err := a.createFlutterwaveSubaccount(seller) // network call - deliberately outside the lock
	if err != nil {
		if strings.HasPrefix(err.Error(), "ALREADY_EXISTS:") {
			// Flutterwave already has a subaccount for this exact bank +
			// account number - almost certainly from an earlier attempt
			// whose ID our local record lost (e.g. a Render restart before
			// DATA_DIR was set up). Recover it instead of leaving the
			// seller stuck.
			existingID, lookupErr := a.findExistingSubaccount(accountNumber)
			if lookupErr != nil {
				log.Printf("payout setup: subaccount already exists for %s but could not recover its ID: %v", accountNumber, lookupErr)
				render(w, "seller_payout.html", map[string]any{
					"User":         seller,
					"Error":        "This bank account is already connected to a Kaya subaccount on Flutterwave, but we couldn't automatically recover it. Contact support rather than retrying.",
					"PaymentsLive": true,
				})
				return
			}
			a.store.mu.Lock()
			seller.FlutterwaveSubaccountID = existingID
			a.store.save()
			a.store.mu.Unlock()
			render(w, "seller_payout.html", map[string]any{
				"User":         seller,
				"Info":         "Payout account reconnected (it already existed from an earlier setup attempt).",
				"PaymentsLive": true,
			})
			return
		}
		render(w, "seller_payout.html", map[string]any{"User": seller, "Error": "Could not set up payouts: " + err.Error(), "PaymentsLive": true})
		return
	}

	a.store.mu.Lock()
	seller.FlutterwaveSubaccountID = result.Data.SubaccountID
	seller.BankAccountName = result.Data.AccountName
	a.store.save()
	a.store.mu.Unlock()

	render(w, "seller_payout.html", map[string]any{
		"User":         seller,
		"Info":         "Payout account connected. You'll now be paid automatically for every sale.",
		"PaymentsLive": true,
	})
}
