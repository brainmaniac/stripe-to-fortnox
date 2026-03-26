package middleware

import (
	"net/http"

	"github.com/alexedwards/scs/v2"
)

func RequireAuth(sm *scs.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !sm.GetBool(r.Context(), "authenticated") {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
