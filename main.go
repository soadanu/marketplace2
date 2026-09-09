package main

import (
	"log"
	"net/http"
	"os"
)

type App struct {
	store    *Store
	payments PaymentConfig
}

func main() {
	os.MkdirAll(dataPath(), 0700)
	os.MkdirAll(dataPath("uploads", "listings"), 0700)
	os.MkdirAll(dataPath("uploads", "kyc"), 0700)
	loadTemplates()

	app := &App{store: NewStore(), payments: loadPaymentConfig()}
	app.ensureAdmin() // creates a default admin account on first run

	if os.Getenv("DATA_DIR") == "" {
		log.Println("WARNING: DATA_DIR is not set - using local './data'. On Render this folder is WIPED on every restart/deploy/idle-spindown. Attach a persistent disk and set DATA_DIR to its mount path before you rely on this in production.")
	}

	if !app.payments.Configured() {
		log.Println("FLW_SECRET_KEY / APP_BASE_URL not set - checkout will run in dev mode (orders marked paid immediately, no real payment taken)")
	}

	mux := http.NewServeMux()

	// Public browsing
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/listing", app.handleListingDetail)

	// Auth
	mux.HandleFunc("/register", methodSplit(app.handleRegisterGet, app.handleRegisterPost))
	mux.HandleFunc("/login", methodSplit(app.handleLoginGet, app.handleLoginPost))
	mux.HandleFunc("/logout", app.handleLogout)
	mux.HandleFunc("/forgot-password", methodSplit(app.handleForgotPasswordGet, app.handleForgotPasswordPost))
	mux.HandleFunc("/reset-password", methodSplit(app.handleResetPasswordGet, app.handleResetPasswordPost))

	// Cart & checkout (cart works for anyone; checkout requires login)
	mux.HandleFunc("/cart", app.handleCartView)
	mux.HandleFunc("/cart/add", app.handleCartAdd)
	mux.HandleFunc("/cart/remove", app.handleCartRemove)
	mux.HandleFunc("/checkout", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleCheckout(w, r, u) }))
	mux.HandleFunc("/buy-now", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleBuyNow(w, r, u) }))
	mux.HandleFunc("/orders", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleOrdersGet(w, r, u) }))
	mux.HandleFunc("/orders/pay", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handlePayOrder(w, r, u) }))
	mux.HandleFunc("/orders/recheck", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleOrderRecheck(w, r, u) }))
	mux.HandleFunc("/payment/callback", app.handlePaymentCallback)
	mux.HandleFunc("/payment/webhook", app.handlePaymentWebhook)

	// Become a seller
	mux.HandleFunc("/seller/apply", app.requireLogin(methodSplitUser(app.handleSellerApplyGet, app.handleSellerApplyPost)))

	// Seller dashboard (approved sellers only)
	mux.HandleFunc("/seller/listings", app.requireApprovedSeller(app.handleSellerListingsGet))
	mux.HandleFunc("/seller/listings/create", app.requireApprovedSeller(app.handleSellerListingCreate))
	mux.HandleFunc("/seller/listings/remove", app.requireApprovedSeller(app.handleSellerListingRemove))
	mux.HandleFunc("/seller/payout", app.requireApprovedSeller(methodSplitUser(app.handleSellerPayoutGet, app.handleSellerPayoutPost)))

	// Admin
	mux.HandleFunc("/admin", app.requireAdmin(app.handleAdminDashboard))
	mux.HandleFunc("/admin/sellers", app.requireAdmin(app.handleAdminSellers))
	mux.HandleFunc("/admin/applications/decide", app.requireAdmin(app.handleAdminApplicationDecision))
	mux.HandleFunc("/admin/categories/create", app.requireAdmin(app.handleAdminCategoryCreate))

	// Uploaded listing images are served publicly; the KYC documents folder
	// is deliberately NOT mounted here - keep those private.
	mux.Handle("/uploads/listings/", http.StripPrefix("/uploads/listings/", http.FileServer(http.Dir(dataPath("uploads", "listings")))))

	addr := ":8080"
	log.Println("Kaya running at http://localhost" + addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// methodSplit routes GET to one handler and POST to another for the same path.
func methodSplit(get, post http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			post(w, r)
			return
		}
		get(w, r)
	}
}

// methodSplitUser is the same idea for handlers that already have a *User.
func methodSplitUser(get, post func(http.ResponseWriter, *http.Request, *User)) func(http.ResponseWriter, *http.Request, *User) {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		if r.Method == http.MethodPost {
			post(w, r, u)
			return
		}
		get(w, r, u)
	}
}

// ensureAdmin creates a default admin login on first run so there's a way
// into /admin without editing the JSON file by hand. Change this password
// immediately after first login.
func (a *App) ensureAdmin() {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	for _, u := range a.store.Users {
		if u.Role == "admin" {
			return
		}
	}
	admin := &User{
		ID:           newID(),
		Name:         "Admin",
		Email:        "admin@kaya.local",
		PasswordHash: hashPassword("changeme123"),
		Role:         "admin",
	}
	a.store.Users[admin.ID] = admin
	a.store.save()
	log.Println("Created default admin: admin@kaya.local / changeme123 - change this password immediately")
}
