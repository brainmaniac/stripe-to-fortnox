package stripe

import (
	stripelib "github.com/stripe/stripe-go/v84"
	"github.com/stripe/stripe-go/v84/client"
)

// Client wraps the Stripe API client.
type Client struct {
	sc *client.API
}

// NewClient creates a new Stripe API client.
func NewClient(apiKey string) *Client {
	sc := &client.API{}
	sc.Init(apiKey, nil)
	return &Client{sc: sc}
}

// API returns the underlying Stripe client.
func (c *Client) API() *client.API {
	return c.sc
}

// SetGlobalAPIKey sets the package-level Stripe API key.
func SetGlobalAPIKey(apiKey string) {
	stripelib.Key = apiKey
}
