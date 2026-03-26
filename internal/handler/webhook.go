package handler

import (
	"net/http"

	stripepkg "stripe-fortnox-sync/internal/stripe"
)

// WebhookHandler wraps the Stripe webhook processor.
type WebhookHandler struct {
	stripeHandler *stripepkg.WebhookHandler
}

func NewWebhookHandler(stripeHandler *stripepkg.WebhookHandler) *WebhookHandler {
	return &WebhookHandler{stripeHandler: stripeHandler}
}

func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	h.stripeHandler.Handle(w, r)
}
