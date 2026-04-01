package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"

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
		FortnoxConnected:    h.fortnoxOAuth.IsConnected(ctx),
		FortnoxClientID:     h.cfg.FortnoxClientID,
		StripeKeySet:        h.cfg.StripeAPIKey != "",
		AccountMappings:     h.loadAccountMappings(ctx),
		SyncIntervalMinutes: h.loadSyncInterval(ctx),
	}
	if err := views.Settings(data).Render(ctx, w); err != nil {
		log.Printf("render settings: %v", err)
	}
}

// UpdateMapping saves konto and momssats for a single account_mappings row.
func (h *SettingsHandler) UpdateMapping(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	konto := r.FormValue("konto")
	momssatsStr := r.FormValue("momssats")

	m, err := h.queries.GetAccountMappingByID(ctx, id)
	if err != nil || m == nil {
		h.renderSettings(w, r, "danger", fmt.Sprintf("Kontomappning %d hittades inte.", id))
		return
	}

	m.Konto = konto
	if momssatsStr == "" {
		m.Momssats.Valid = false
	} else {
		v, err := strconv.ParseFloat(momssatsStr, 64)
		if err != nil {
			h.renderSettings(w, r, "danger", "Ogiltig momssats: "+momssatsStr)
			return
		}
		m.Momssats.Float64 = v
		m.Momssats.Valid = true
	}

	if err := h.queries.UpdateAccountMapping(ctx, *m); err != nil {
		log.Printf("update mapping %d: %v", id, err)
		h.renderSettings(w, r, "danger", "Kunde inte spara: "+err.Error())
		return
	}
	h.renderSettings(w, r, "success", "Kontomappning sparad.")
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
		FortnoxConnected:    h.fortnoxOAuth.IsConnected(ctx),
		FortnoxClientID:     h.cfg.FortnoxClientID,
		StripeKeySet:        h.cfg.StripeAPIKey != "",
		FlashKind:           flashKind,
		FlashMsg:            flashMsg,
		AccountMappings:     h.loadAccountMappings(ctx),
		SyncIntervalMinutes: h.loadSyncInterval(ctx),
	}
	views.Settings(data).Render(ctx, w)
}

func (h *SettingsHandler) loadAccountMappings(ctx context.Context) []views.AccountMappingView {
	mappings, err := h.queries.ListAccountMappings(ctx)
	if err != nil {
		log.Printf("list account mappings: %v", err)
		return nil
	}
	result := make([]views.AccountMappingView, len(mappings))
	for i, m := range mappings {
		momssats := ""
		if m.Momssats.Valid {
			momssats = strconv.FormatFloat(m.Momssats.Float64, 'f', -1, 64)
		}
		result[i] = views.AccountMappingView{
			ID:       m.ID,
			Kontotyp: m.Kontotyp,
			Matchtyp: m.Matchtyp,
			Matchkod: m.Matchkod,
			Konto:    m.Konto,
			Momssats: momssats,
		}
	}
	return result
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
