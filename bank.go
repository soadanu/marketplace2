package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

type Bank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type flwBankListResponse struct {
	Status string `json:"status"`
	Data   []Bank `json:"data"`
}

// fetchNigerianBanks asks Flutterwave for the current list of banks and
// their codes, used to populate the dropdown on the seller payout form.
func (a *App) fetchNigerianBanks() ([]Bank, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.flutterwave.com/v3/banks/NG", nil)
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
	var parsed flwBankListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not parse bank list: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("Flutterwave did not return a bank list")
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Name < parsed.Data[j].Name })
	return parsed.Data, nil
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
