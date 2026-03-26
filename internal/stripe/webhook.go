package stripe

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"

	stripelib "github.com/stripe/stripe-go/v84"
	"github.com/stripe/stripe-go/v84/webhook"
	"stripe-fortnox-sync/internal/db"
)

// WebhookHandler processes incoming Stripe webhook events.
type WebhookHandler struct {
	webhookSecret string
	queries       *db.Queries
	syncer        *Syncer
}

func NewWebhookHandler(webhookSecret string, queries *db.Queries, syncer *Syncer) *WebhookHandler {
	return &WebhookHandler{
		webhookSecret: webhookSecret,
		queries:       queries,
		syncer:        syncer,
	}
}

func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	const maxBodyBytes = int64(65536)
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "unable to read body", http.StatusBadRequest)
		return
	}

	var event stripelib.Event
	if h.webhookSecret != "" {
		event, err = webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.webhookSecret)
		if err != nil {
			log.Printf("webhook signature verification failed: %v", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
	} else {
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	switch event.Type {
	case "charge.succeeded", "charge.updated", "charge.refunded":
		var charge stripelib.Charge
		if err := json.Unmarshal(event.Data.Raw, &charge); err == nil {
			h.upsertCharge(ctx, &charge)
		}
	case "payout.paid", "payout.updated", "payout.reconciliation_completed":
		var payout stripelib.Payout
		if err := json.Unmarshal(event.Data.Raw, &payout); err == nil {
			h.upsertPayout(ctx, &payout)
			// reconciliation_completed means Stripe has finished attributing
			// balance transactions to this payout — safe to query them now.
			if string(payout.Status) == "paid" || event.Type == "payout.reconciliation_completed" {
				go func() {
					if err := h.syncer.SyncBalanceTransactionsForPayout(context.Background(), payout.ID); err != nil {
						log.Printf("webhook sync balance txns: %v", err)
					}
				}()
			}
		}
	case "customer.created", "customer.updated":
		var customer stripelib.Customer
		if err := json.Unmarshal(event.Data.Raw, &customer); err == nil {
			country := ""
			if customer.Address != nil {
				country = customer.Address.Country
			}
			if err := h.queries.UpsertStripeCustomer(ctx, customer.ID, customer.Email, customer.Name, country, customer.Created); err != nil {
				log.Printf("webhook upsert customer %s: %v", customer.ID, err)
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (h *WebhookHandler) upsertCharge(ctx context.Context, c *stripelib.Charge) {
	// Re-use the shared mapping function so field handling stays in sync with the bulk syncer.
	charge := chargeFromStripe(c)
	if err := h.queries.UpsertStripeCharge(ctx, charge); err != nil {
		log.Printf("webhook upsert charge %s: %v", c.ID, err)
	}
}

func (h *WebhookHandler) upsertPayout(ctx context.Context, p *stripelib.Payout) {
	payout := db.StripePayout{
		ID:          p.ID,
		Amount:      p.Amount,
		Currency:    string(p.Currency),
		ArrivalDate: p.ArrivalDate,
		Status:      string(p.Status),
		CreatedAt:   p.Created,
	}
	if p.Description != "" {
		payout.Description = sql.NullString{String: p.Description, Valid: true}
	}
	if err := h.queries.UpsertStripePayout(ctx, payout); err != nil {
		log.Printf("webhook upsert payout %s: %v", p.ID, err)
	}
}
