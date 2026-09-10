package main

import "time"

type User struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"password_hash"`
	Phone        string    `json:"phone"`
	Role         string    `json:"role"`          // "buyer", "seller", "admin"
	SellerStatus string    `json:"seller_status"` // "", "pending", "approved", "rejected"
	CreatedAt    time.Time `json:"created_at"`

	// Payout details - only relevant once SellerStatus == "approved".
	// FlutterwaveSubaccountID being non-empty means Flutterwave will
	// automatically settle this seller's share of every sale straight to
	// their bank account; until it's set, they can't be paid via checkout.
	BankCode                string `json:"bank_code"` // Flutterwave bank ISO code, e.g. "044"
	BankName                string `json:"bank_name"` // display name, e.g. "Access Bank"
	BankAccountNumber       string `json:"bank_account_number"`
	BankAccountName         string `json:"bank_account_name"` // name returned by Flutterwave when the account was resolved
	FlutterwaveSubaccountID string `json:"flutterwave_subaccount_id"`
}

type Session struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PasswordReset struct {
	Token     string    `json:"token"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

type SellerApplication struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	NIN         string    `json:"nin"`
	NINDocPath  string    `json:"nin_doc_path"`
	AltIDType   string    `json:"alt_id_type"`
	AltIDValue  string    `json:"alt_id_value"`
	Status      string    `json:"status"` // "pending", "approved", "rejected"
	SubmittedAt time.Time `json:"submitted_at"`
	ReviewedBy  string    `json:"reviewed_by"`
}

type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Listing struct {
	ID          string    `json:"id"`
	SellerID    string    `json:"seller_id"`
	CategoryID  string    `json:"category_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	PriceKobo   int       `json:"price_kobo"`
	Stock       int       `json:"stock"`
	ImagePaths  []string  `json:"image_paths"` // up to maxListingImages - first one is the cover shown on the home grid
	Status      string    `json:"status"`      // "active", "sold_out", "removed"
	CreatedAt   time.Time `json:"created_at"`
}

// Conversation is a chat thread - either between a buyer and a seller
// (optionally about a specific listing), or a buyer/seller and general
// support (SellerID == "support" in that case).
type Conversation struct {
	ID        string    `json:"id"`
	BuyerID   string    `json:"buyer_id"`
	SellerID  string    `json:"seller_id"` // "support" for the general customer care thread
	ListingID string    `json:"listing_id"`
	CreatedAt time.Time `json:"created_at"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	SenderID       string    `json:"sender_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}

type Order struct {
	ID               string      `json:"id"`
	CheckoutGroupID  string      `json:"checkout_group_id"` // links orders created from the same cart when it spanned multiple sellers
	BuyerID          string      `json:"buyer_id"`
	SellerID         string      `json:"seller_id"` // every item in an Order belongs to one seller - see checkout grouping in order.go
	TotalKobo        int         `json:"total_kobo"`
	PlatformFeeKobo  int         `json:"platform_fee_kobo"` // informational - Flutterwave computes the real split itself
	SellerPayoutKobo int         `json:"seller_payout_kobo"`
	Status           string      `json:"status"` // "pending_payment", "paid", "payment_failed", "shipped", "delivered"
	PaymentRef       string      `json:"payment_ref"`
	Items            []OrderItem `json:"items"`
	OversoldWarning  string      `json:"oversold_warning,omitempty"` // set if stock ran out between order and confirmed payment - needs manual admin follow-up
	CreatedAt        time.Time   `json:"created_at"`
}

type OrderItem struct {
	ListingID string `json:"listing_id"`
	SellerID  string `json:"seller_id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	PriceKobo int    `json:"price_kobo"` // price at time of purchase
}
