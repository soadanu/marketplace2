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
	ImagePath   string    `json:"image_path"`
	Status      string    `json:"status"` // "active", "sold_out", "removed"
	CreatedAt   time.Time `json:"created_at"`
}

type Order struct {
	ID           string      `json:"id"`
	BuyerID      string      `json:"buyer_id"`
	TotalKobo    int         `json:"total_kobo"`
	Status       string      `json:"status"` // "placed", "paid", "shipped", "delivered"
	PaymentRef   string      `json:"payment_ref"`
	Items        []OrderItem `json:"items"`
	CreatedAt    time.Time   `json:"created_at"`
}

type OrderItem struct {
	ListingID string `json:"listing_id"`
	SellerID  string `json:"seller_id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	PriceKobo int    `json:"price_kobo"` // price at time of purchase
}
