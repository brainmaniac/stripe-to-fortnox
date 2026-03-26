package handler

import (
	"net/http"

	"github.com/alexedwards/scs/v2"
	"golang.org/x/crypto/bcrypt"

	"stripe-fortnox-sync/internal/views"
)

// AuthHandler handles login/logout.
type AuthHandler struct {
	sm           *scs.SessionManager
	passwordHash string
}

func NewAuthHandler(sm *scs.SessionManager, passwordHash string) *AuthHandler {
	return &AuthHandler{sm: sm, passwordHash: passwordHash}
}

func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	views.Login("").Render(r.Context(), w)
}

func (h *AuthHandler) LoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if err := bcrypt.CompareHashAndPassword([]byte(h.passwordHash), []byte(password)); err != nil {
		views.Login("Invalid password").Render(r.Context(), w)
		return
	}

	if err := h.sm.RenewToken(r.Context()); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	h.sm.Put(r.Context(), "authenticated", true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.sm.Destroy(r.Context()); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
