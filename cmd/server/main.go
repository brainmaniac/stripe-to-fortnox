package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"

	"stripe-fortnox-sync/internal/config"
	"stripe-fortnox-sync/internal/database"
	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/fortnox"
	"stripe-fortnox-sync/internal/handler"
	appmiddleware "stripe-fortnox-sync/internal/middleware"
	"stripe-fortnox-sync/internal/scheduler"
	stripepkg "stripe-fortnox-sync/internal/stripe"
)

func main() {
	// Load .env file if present (ignored in production where env vars are set directly)
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	sqlDB, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer sqlDB.Close()

	if err := database.RunMigrations(sqlDB); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	queries := db.New(sqlDB)

	// Session manager
	sessionManager := scs.New()
	sessionManager.Lifetime = 24 * time.Hour
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteNoneMode
	sessionManager.Cookie.Secure = true

	// Stripe
	stripeSyncer := stripepkg.NewSyncer(cfg.StripeAPIKey, queries)
	stripeWebhookHandler := stripepkg.NewWebhookHandler(cfg.StripeWebhookSecret, queries, stripeSyncer)

	// Fortnox
	fortnoxOAuth := fortnox.NewOAuthClient(cfg.FortnoxClientID, cfg.FortnoxClientSecret, cfg.BaseURL, queries)
	fortnoxOAuth.StartTokenRefresher(context.Background())
	fortnoxAPI := fortnox.NewAPIClient(fortnoxOAuth)
	voucherCreator := fortnox.NewVoucherCreator(fortnoxAPI, queries, fortnox.DefaultAccountConfig())
	mappingResolver := fortnox.NewMappingResolver(queries)
	invoiceService := fortnox.NewInvoiceService(fortnoxAPI, queries, mappingResolver)

	// Scheduler
	sched := scheduler.New(queries, stripeSyncer, voucherCreator, invoiceService)
	sched.Start(context.Background())

	// Handlers
	authHandler := handler.NewAuthHandler(sessionManager, cfg.AdminPasswordHash)
	dashboardHandler := handler.NewDashboardHandler(queries, fortnoxOAuth)
	syncHandler := handler.NewSyncHandler(queries, stripeSyncer, voucherCreator, invoiceService)
	settingsHandler := handler.NewSettingsHandler(queries, fortnoxOAuth, cfg, sessionManager)
	webhookHandler := handler.NewWebhookHandler(stripeWebhookHandler)

	// Router
	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(sessionManager.LoadAndSave)

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Post("/webhook/stripe", webhookHandler.Handle)
	r.Get("/login", authHandler.LoginPage)
	r.Post("/login", authHandler.LoginPost)
	r.Get("/auth/fortnox/callback", settingsHandler.FortnoxCallback)

	r.Group(func(r chi.Router) {
		r.Use(appmiddleware.RequireAuth(sessionManager))
		r.Get("/", dashboardHandler.Dashboard)
		r.Post("/logout", authHandler.Logout)
		r.Get("/settings", settingsHandler.Settings)
		r.Post("/settings/accounts", settingsHandler.CreateMapping)
		r.Post("/settings/accounts/{id}", settingsHandler.UpdateMapping)
		r.Post("/settings/sync-interval", settingsHandler.SaveSyncInterval)
		r.Post("/settings/fortnox/disconnect", settingsHandler.FortnoxDisconnect)
		r.Get("/auth/fortnox", settingsHandler.FortnoxAuthorize)
		r.Post("/sync/stripe", syncHandler.TriggerStripeSync)
		r.Post("/sync/fortnox", syncHandler.TriggerFortnoxSync)
		r.Post("/sync/fortnox/retry/{id}", syncHandler.RetryPendingVoucher)
		r.Get("/sync/status", syncHandler.SyncStatus)
		r.Get("/sync", syncHandler.SyncPage)
		r.Get("/vouchers", syncHandler.ListVouchers)
		r.Get("/customers", syncHandler.ListCustomers)
		r.Get("/charges", syncHandler.ListCharges)
		r.Get("/payouts", syncHandler.ListPayouts)
		r.Get("/logs", syncHandler.SyncLogs)
	})

	addr := fmt.Sprintf(":%s", cfg.AppPort)
	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server: %v", err)
	}
}
