package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"

	"github.com/alexedwards/scs/v2"

	"stripe-fortnox-sync/internal/config"
	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/fortnox"
	"stripe-fortnox-sync/internal/views"
)

// SettingsHandler handles the settings page and Fortnox OAuth flow.
type SettingsHandler struct {
	queries      *db.Queries
	fortnoxOAuth *fortnox.OAuthClient
	cfg          *config.Config
	sm           *scs.SessionManager
}

func NewSettingsHandler(
	queries *db.Queries,
	fortnoxOAuth *fortnox.OAuthClient,
	cfg *config.Config,
	sm *scs.SessionManager,
) *SettingsHandler {
	return &SettingsHandler{
		queries:      queries,
		fortnoxOAuth: fortnoxOAuth,
		cfg:          cfg,
		sm:           sm,
	}
}

func (h *SettingsHandler) Settings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := views.SettingsData{
		FortnoxConnected: h.fortnoxOAuth.IsConnected(ctx),
		FortnoxClientID:  h.cfg.FortnoxClientID,
		StripeKeySet:     h.cfg.StripeAPIKey != "",
		AccountConfig:       h.loadAccountConfig(ctx),
		SyncIntervalMinutes: h.loadSyncInterval(ctx),
	}
	if err := views.Settings(data).Render(ctx, w); err != nil {
		log.Printf("render settings: %v", err)
	}
}

func (h *SettingsHandler) SaveAccountSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fields := map[string]string{
		"account_stripe_clearing":    r.FormValue("stripe_clearing"),
		"account_bank_account":       r.FormValue("bank_account"),
		"account_revenue_se":         r.FormValue("revenue_se"),
		"account_revenue_eu":         r.FormValue("revenue_eu"),
		"account_revenue_wo":         r.FormValue("revenue_wo"),
		"account_output_vat25":       r.FormValue("output_vat25"),
		"account_payment_fee":        r.FormValue("payment_fee"),
		"account_reverse_vat_debit":  r.FormValue("reverse_vat_debit"),
		"account_reverse_vat_credit": r.FormValue("reverse_vat_credit"),
		"voucher_series":             r.FormValue("voucher_series"),
		"vat_percent":                r.FormValue("vat_percent"),
	}

	for key, value := range fields {
		if value == "" {
			continue
		}
		if err := h.queries.UpsertSetting(ctx, key, value, 0); err != nil {
			log.Printf("save setting %s: %v", key, err)
			h.renderSettings(w, r, "danger", "Failed to save settings: "+err.Error())
			return
		}
	}
	h.renderSettings(w, r, "success", "Account settings saved.")
}

func (h *SettingsHandler) FortnoxAuthorize(w http.ResponseWriter, r *http.Request) {
	state := randomHex(16)
	h.sm.Put(r.Context(), "fortnox_oauth_state", state)
	http.Redirect(w, r, h.fortnoxOAuth.AuthorizeURL(state), http.StatusSeeOther)
}

func (h *SettingsHandler) FortnoxCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	storedState := h.sm.GetString(ctx, "fortnox_oauth_state")
	if storedState == "" || storedState != state {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	h.sm.Remove(ctx, "fortnox_oauth_state")

	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	tr, err := h.fortnoxOAuth.ExchangeCode(ctx, code)
	if err != nil {
		log.Printf("fortnox oauth exchange: %v", err)
		http.Error(w, "OAuth exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.fortnoxOAuth.SaveTokens(ctx, tr.AccessToken, tr.RefreshToken, tr.ExpiresIn); err != nil {
		log.Printf("save fortnox tokens: %v", err)
		http.Error(w, "failed to save tokens", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SettingsHandler) FortnoxDisconnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	for _, key := range []string{"fortnox_access_token", "fortnox_refresh_token", "fortnox_token_expiry"} {
		if err := h.queries.DeleteSetting(ctx, key); err != nil {
			log.Printf("delete setting %s: %v", key, err)
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *SettingsHandler) renderSettings(w http.ResponseWriter, r *http.Request, flashKind, flashMsg string) {
	ctx := r.Context()
	data := views.SettingsData{
		FortnoxConnected: h.fortnoxOAuth.IsConnected(ctx),
		FortnoxClientID:  h.cfg.FortnoxClientID,
		StripeKeySet:     h.cfg.StripeAPIKey != "",
		FlashKind:        flashKind,
		FlashMsg:         flashMsg,
		AccountConfig:       h.loadAccountConfig(ctx),
		SyncIntervalMinutes: h.loadSyncInterval(ctx),
	}
	views.Settings(data).Render(ctx, w)
}

func (h *SettingsHandler) loadAccountConfig(ctx context.Context) views.AccountConfigForm {
	get := func(key, def string) string {
		s, err := h.queries.GetSetting(ctx, key)
		if err != nil || s == nil {
			return def
		}
		return s.Value
	}
	return views.AccountConfigForm{
		StripeClearing:   get("account_stripe_clearing", fortnox.AccountStripeClearing),
		BankAccount:      get("account_bank_account", fortnox.AccountBankAccount),
		RevenueSE:        get("account_revenue_se", fortnox.AccountRevenueSE),
		RevenueEU:        get("account_revenue_eu", fortnox.AccountRevenueEU),
		RevenueWO:        get("account_revenue_wo", fortnox.AccountRevenueWO),
		OutputVAT25:      get("account_output_vat25", fortnox.AccountOutputVAT25),
		PaymentFee:       get("account_payment_fee", fortnox.AccountPaymentFee),
		ReverseVATDebit:  get("account_reverse_vat_debit", fortnox.AccountReverseVATDebit),
		ReverseVATCredit: get("account_reverse_vat_credit", fortnox.AccountReverseVATCredit),
		VoucherSeries:    get("voucher_series", "A"),
		VATPercent:       get("vat_percent", strconv.FormatFloat(25.0, 'f', -1, 64)),
	}
}

func (h *SettingsHandler) loadSyncInterval(ctx context.Context) string {
	s, err := h.queries.GetSetting(ctx, "sync_interval_minutes")
	if err != nil || s == nil {
		return "60"
	}
	return s.Value
}

func (h *SettingsHandler) SaveSyncInterval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	val := r.FormValue("sync_interval_minutes")
	if val == "" {
		val = "60"
	}
	if err := h.queries.UpsertSetting(ctx, "sync_interval_minutes", val, 0); err != nil {
		log.Printf("save sync interval: %v", err)
		h.renderSettings(w, r, "danger", "Kunde inte spara intervall: "+err.Error())
		return
	}
	h.renderSettings(w, r, "success", "Synkintervall sparat.")
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
