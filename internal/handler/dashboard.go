package handler

import (
	"context"
	"log"
	"net/http"

	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/fortnox"
	"stripe-fortnox-sync/internal/views"
)

// DashboardHandler serves the main dashboard page.
type DashboardHandler struct {
	queries       *db.Queries
	fortnoxOAuth  *fortnox.OAuthClient
}

func NewDashboardHandler(queries *db.Queries, fortnoxOAuth *fortnox.OAuthClient) *DashboardHandler {
	return &DashboardHandler{queries: queries, fortnoxOAuth: fortnoxOAuth}
}

func (h *DashboardHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := views.DashboardData{}

	data.FortnoxConnected = h.fortnoxOAuth.IsConnected(ctx)
	data.TotalCustomers = h.countSafe(ctx, h.queries.CountStripeCustomers)
	data.TotalCharges = h.countSafe(ctx, h.queries.CountStripeCharges)
	data.TotalPayouts = h.countSafe(ctx, h.queries.CountStripePayouts)
	data.TotalVouchers = h.countSafe(ctx, h.queries.CountFortnoxVouchers)
	data.UnsyncedCharges = h.countSafe(ctx, h.queries.CountUnsyncedCharges)
	data.UnsyncedPayouts = h.countSafe(ctx, h.queries.CountUnsyncedPayouts)

	states, err := h.queries.ListSyncStates(ctx)
	if err != nil {
		log.Printf("list sync states: %v", err)
	}
	data.SyncStates = states

	logs, err := h.queries.ListRecentSyncLogs(ctx, 20)
	if err != nil {
		log.Printf("list recent logs: %v", err)
	}
	data.RecentLogs = logs

	if err := views.Dashboard(data).Render(ctx, w); err != nil {
		log.Printf("render dashboard: %v", err)
	}
}

func (h *DashboardHandler) countSafe(ctx context.Context, fn func(context.Context) (int64, error)) int64 {
	n, err := fn(ctx)
	if err != nil {
		return 0
	}
	return n
}
