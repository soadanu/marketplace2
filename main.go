package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type App struct {
	store     *Store
	payments  PaymentConfig
	email     EmailConfig
	limits    *limits
	emailTest emailTestState
}

func main() {
	os.MkdirAll(dataPath(), 0700)
	os.MkdirAll(dataPath("uploads", "listings"), 0700)
	os.MkdirAll(dataPath("uploads", "kyc"), 0700)
	loadTemplates()

	app := &App{store: NewStore(), payments: loadPaymentConfig(), email: loadEmailConfig(), limits: newLimits()}
	app.ensureAdmin() // creates a default admin account on first run

	if os.Getenv("DATA_DIR") == "" {
		log.Println("WARNING: DATA_DIR is not set - using local './data'. On Render this folder is WIPED on every restart/deploy/idle-spindown. Attach a persistent disk and set DATA_DIR to its mount path before you rely on this in production.")
	}

	if !app.payments.Configured() {
		log.Println("FLW_SECRET_KEY / APP_BASE_URL not set - checkout will run in dev mode (orders marked paid immediately, no real payment taken)")
	}

	switch app.email.Mode() {
	case "none":
		log.Println("Email is not set up (BREVO_API_KEY + EMAIL_FROM) - password reset links and order updates will only be written to this log, not emailed")
	case "smtp":
		if os.Getenv("RENDER") != "" {
			log.Println("Email is sending over SMTP. Render's FREE plan blocks SMTP ports (25, 465, 587): if emails don't arrive, use BREVO_API_KEY + EMAIL_FROM instead, or move to a paid plan")
		}
	}

	mux := http.NewServeMux()

	// Public browsing
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/listing", app.handleListingDetail)

	// Auth
	mux.HandleFunc("/register", methodSplit(app.handleRegisterGet, app.handleRegisterPost))
	mux.HandleFunc("/login", methodSplit(app.handleLoginGet, app.handleLoginPost))
	mux.HandleFunc("/logout", app.handleLogout)
	mux.HandleFunc("/terms", app.handleTerms)
	mux.HandleFunc("/forgot-password", methodSplit(app.handleForgotPasswordGet, app.handleForgotPasswordPost))
	mux.HandleFunc("/reset-password", methodSplit(app.handleResetPasswordGet, app.handleResetPasswordPost))

	// Cart & checkout (cart works for anyone; checkout requires login)
	mux.HandleFunc("/cart", app.handleCartView)
	mux.HandleFunc("/cart/add", app.handleCartAdd)
	mux.HandleFunc("/cart/remove", app.handleCartRemove)
	mux.HandleFunc("/checkout", app.requireLogin(methodSplitUser(app.handleCheckoutGet, app.handleCheckout)))
	mux.HandleFunc("/buy-now", app.requireLogin(app.handleBuyNow)) // GET shows the delivery form, POST places the order
	mux.HandleFunc("/orders", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleOrdersGet(w, r, u) }))
	mux.HandleFunc("/orders/pay", app.requireLogin(postOnly(func(w http.ResponseWriter, r *http.Request, u *User) { app.handlePayOrder(w, r, u) })))
	mux.HandleFunc("/orders/recheck", app.requireLogin(postOnly(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleOrderRecheck(w, r, u) })))
	mux.HandleFunc("/payment/callback", app.handlePaymentCallback)
	mux.HandleFunc("/payment/webhook", app.handlePaymentWebhook)

	// Become a seller
	mux.HandleFunc("/seller/apply", app.requireLogin(methodSplitUser(app.handleSellerApplyGet, app.handleSellerApplyPost)))

	// Seller dashboard (approved sellers only)
	mux.HandleFunc("/seller/listings", app.requireApprovedSeller(app.handleSellerListingsGet))
	mux.HandleFunc("/seller/listings/create", app.requireApprovedSeller(app.handleSellerListingCreate))
	mux.HandleFunc("/seller/listings/remove", app.requireApprovedSeller(postOnly(app.handleSellerListingRemove)))
	mux.HandleFunc("/seller/payout", app.requireApprovedSeller(methodSplitUser(app.handleSellerPayoutGet, app.handleSellerPayoutPost)))

	// Admin
	mux.HandleFunc("/admin", app.requireAdmin(app.handleAdminDashboard))
	mux.HandleFunc("/admin/sellers", app.requireAdmin(app.handleAdminSellers))
	mux.HandleFunc("/admin/applications/decide", app.requireAdmin(postOnly(app.handleAdminApplicationDecision)))
	mux.HandleFunc("/admin/categories/create", app.requireAdmin(postOnly(app.handleAdminCategoryCreate)))
	mux.HandleFunc("/admin/support", app.requireAdmin(app.handleAdminSupport))

	// Chat / customer care
	mux.HandleFunc("/chat", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleChatOpen(w, r, u) }))
	mux.HandleFunc("/chat/view", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleChatView(w, r, u) }))
	mux.HandleFunc("/chat/send", app.requireLogin(postOnly(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleChatSend(w, r, u) })))
	mux.HandleFunc("/inbox", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleInbox(w, r, u) }))
	mux.HandleFunc("/support", app.requireLogin(func(w http.ResponseWriter, r *http.Request, u *User) { app.handleSupportOpen(w, r, u) }))

	mux.HandleFunc("/chat/poll", app.requireLogin(app.handleChatPoll))

	// Info pages and small JSON helpers
	mux.HandleFunc("/privacy", app.handlePrivacy)
	mux.HandleFunc("/shop", app.handleShop)
	mux.HandleFunc("/api/counts", app.handleCounts)

	// Account
	mux.HandleFunc("/account", app.requireLogin(methodSplitUser(app.handleAccountGet, app.handleAccountPost)))
	mux.HandleFunc("/account/password", app.requireLogin(postOnly(app.handlePasswordChange)))
	mux.HandleFunc("/account/email", app.requireLogin(postOnly(app.handleEmailChange)))

	// Delivery, reviews, saved items, reports
	mux.HandleFunc("/orders/received", app.requireLogin(postOnly(app.handleOrderReceived)))
	mux.HandleFunc("/reviews/create", app.requireLogin(postOnly(app.handleReviewCreate)))
	mux.HandleFunc("/favorites", app.requireLogin(app.handleFavorites))
	mux.HandleFunc("/favorites/toggle", app.requireLogin(postOnly(app.handleFavoriteToggle)))
	mux.HandleFunc("/report", app.requireLogin(methodSplitUser(app.handleReportGet, app.handleReportPost)))

	// Seller: orders to ship, edit listings
	mux.HandleFunc("/seller/orders", app.requireApprovedSeller(app.handleSellerOrders))
	mux.HandleFunc("/seller/orders/ship", app.requireApprovedSeller(postOnly(app.handleSellerShip)))
	mux.HandleFunc("/seller/listings/edit", app.requireApprovedSeller(methodSplitUser(app.handleSellerListingEditGet, app.handleSellerListingEditPost)))

	// Admin tools
	mux.HandleFunc("/admin/orders", app.requireAdmin(app.handleAdminOrders))
	mux.HandleFunc("/admin/listings", app.requireAdmin(app.handleAdminListings))
	mux.HandleFunc("/admin/listings/status", app.requireAdmin(postOnly(app.handleAdminListingStatus)))
	mux.HandleFunc("/admin/users", app.requireAdmin(app.handleAdminUsers))
	mux.HandleFunc("/admin/users/suspend", app.requireAdmin(postOnly(app.handleAdminUserSuspend)))
	mux.HandleFunc("/admin/reports", app.requireAdmin(app.handleAdminReports))
	mux.HandleFunc("/admin/reports/resolve", app.requireAdmin(postOnly(app.handleAdminReportResolve)))
	mux.HandleFunc("/admin/kyc", app.requireAdmin(app.handleAdminKYC))
	mux.HandleFunc("/admin/email-test", app.requireAdmin(postOnly(app.handleAdminEmailTest)))

	// Uploaded listing images are served publicly; the KYC documents folder
	// is deliberately NOT mounted here - keep those private.
	mux.Handle("/uploads/listings/", http.StripPrefix("/uploads/listings/", safeImages(http.FileServer(http.Dir(dataPath("uploads", "listings"))))))

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

// postOnly refuses anything but POST for handlers that change data, so a
// link or image on another site can't trigger them with a plain GET.
func postOnly(next func(http.ResponseWriter, *http.Request, *User)) func(http.ResponseWriter, *http.Request, *User) {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r, u)
	}
}

// safeImages serves uploaded photos only when the file name ends in an image
// extension, and tells browsers not to guess a different type, so an upload
// can never be served as a web page.
func safeImages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(filepath.Ext(r.URL.Path)) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// ensureAdmin creates the first admin login if there is none. Set
// ADMIN_EMAIL and ADMIN_PASSWORD to choose them; otherwise a well-known
// default is used and the admin dashboard nags until it is changed.
func (a *App) ensureAdmin() {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()

	for _, u := range a.store.Users {
		if u.Role == "admin" {
			return
		}
	}
	email := strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_EMAIL")))
	if email == "" {
		email = "admin@kaya.local"
	}
	password := os.Getenv("ADMIN_PASSWORD")
	usingDefault := password == ""
	if usingDefault {
		password = "changeme123"
	}
	admin := &User{
		ID:           newID(),
		Name:         "Admin",
		Email:        email,
		PasswordHash: hashPassword(password),
		Role:         "admin",
	}
	a.store.Users[admin.ID] = admin
	a.store.save()
	if usingDefault {
		log.Printf("Created default admin: %s / changeme123 - change this password immediately (or set ADMIN_EMAIL and ADMIN_PASSWORD)", email)
	} else {
		log.Printf("Created admin %s from ADMIN_EMAIL / ADMIN_PASSWORD", email)
	}
}
