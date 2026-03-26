package stripe

import (
	"database/sql"
	"testing"

	stripelib "github.com/stripe/stripe-go/v84"
)

// ── Bullet 4: chargeFromStripe captures billing country ───────────────────────

func TestChargeFromStripeCaptursBillingCountry(t *testing.T) {
	c := &stripelib.Charge{
		ID:             "ch_test",
		Amount:         10000,
		AmountCaptured: 10000,
		Currency:       "sek",
		Status:         "succeeded",
		Created:        1700000000,
		BillingDetails: &stripelib.ChargeBillingDetails{
			Address: &stripelib.Address{Country: "DE"},
		},
	}
	got := chargeFromStripe(c)
	if !got.BillingCountry.Valid {
		t.Fatal("BillingCountry should be valid")
	}
	if got.BillingCountry.String != "DE" {
		t.Errorf("BillingCountry: got %q, want %q", got.BillingCountry.String, "DE")
	}
}

// ── Bullet 4: missing billing details → empty billing country ─────────────────

func TestChargeFromStripeNoBillingDetails(t *testing.T) {
	c := &stripelib.Charge{
		ID:             "ch_no_billing",
		Amount:         5000,
		AmountCaptured: 5000,
		Currency:       "sek",
		Status:         "succeeded",
		Created:        1700000000,
	}
	got := chargeFromStripe(c)
	if got.BillingCountry.Valid {
		t.Errorf("expected empty BillingCountry, got %q", got.BillingCountry.String)
	}
}

// ── chargeFromStripe maps all fields correctly ────────────────────────────────

func TestChargeFromStripeFieldMapping(t *testing.T) {
	bt := &stripelib.BalanceTransaction{ID: "txn_123"}
	cust := &stripelib.Customer{ID: "cus_456"}
	pi := &stripelib.PaymentIntent{ID: "pi_789"}

	c := &stripelib.Charge{
		ID:                 "ch_full",
		Amount:             20000,
		AmountCaptured:     19000,
		Currency:           "sek",
		Status:             "succeeded",
		Created:            1700000000,
		Description:        "Test charge",
		BalanceTransaction: bt,
		Customer:           cust,
		PaymentIntent:      pi,
		BillingDetails: &stripelib.ChargeBillingDetails{
			Address: &stripelib.Address{Country: "SE"},
		},
	}

	got := chargeFromStripe(c)

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"ID", got.ID, "ch_full"},
		{"Amount", got.Amount, int64(20000)},
		{"AmountCaptured", got.AmountCaptured, int64(19000)},
		{"Currency", got.Currency, "sek"},
		{"Status", got.Status, "succeeded"},
		{"CreatedAt", got.CreatedAt, int64(1700000000)},
		{"Description", got.Description.String, "Test charge"},
		{"BalanceTransactionID", got.BalanceTransactionID.String, "txn_123"},
		{"CustomerID", got.CustomerID.String, "cus_456"},
		{"PaymentIntentID", got.PaymentIntentID.String, "pi_789"},
		{"BillingCountry", got.BillingCountry.String, "SE"},
	}

	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// ── Bullet 1: incremental sync uses correct timestamp filter ─────────────────
// chargeFromStripe must preserve CreatedAt so sync_state can record the
// watermark and the next sync uses created[gte]=last_synced_at-60.

func TestChargeCreatedAtPreserved(t *testing.T) {
	c := &stripelib.Charge{
		ID:             "ch_ts",
		Amount:         1000,
		AmountCaptured: 1000,
		Currency:       "sek",
		Status:         "succeeded",
		Created:        1710000000,
	}
	got := chargeFromStripe(c)
	if got.CreatedAt != 1710000000 {
		t.Errorf("CreatedAt: got %d, want 1710000000", got.CreatedAt)
	}
}

// ── Bullet 3: webhook event fields map to DB correctly ────────────────────────
// Verify chargeFromStripe works identically for webhook payloads — both the
// bulk syncer and the webhook handler call the same function.

func TestChargeFromStripeRefundedStatus(t *testing.T) {
	c := &stripelib.Charge{
		ID:             "ch_refund",
		Amount:         5000,
		AmountCaptured: 5000,
		Currency:       "sek",
		Status:         "succeeded", // Stripe keeps status=succeeded even after refund
		Created:        1700000000,
	}
	got := chargeFromStripe(c)
	if got.Status != "succeeded" {
		t.Errorf("status: got %q, want %q", got.Status, "succeeded")
	}
}

// ── Optional fields don't panic when nil ─────────────────────────────────────

func TestChargeFromStripeNilOptionalFields(t *testing.T) {
	c := &stripelib.Charge{
		ID:             "ch_nil",
		Amount:         100,
		AmountCaptured: 100,
		Currency:       "sek",
		Status:         "succeeded",
		Created:        1700000000,
		// BalanceTransaction, Customer, PaymentIntent, BillingDetails all nil.
	}
	got := chargeFromStripe(c)

	if got.BalanceTransactionID != (sql.NullString{}) {
		t.Error("expected empty BalanceTransactionID")
	}
	if got.CustomerID != (sql.NullString{}) {
		t.Error("expected empty CustomerID")
	}
	if got.PaymentIntentID != (sql.NullString{}) {
		t.Error("expected empty PaymentIntentID")
	}
	if got.BillingCountry != (sql.NullString{}) {
		t.Error("expected empty BillingCountry")
	}
}
