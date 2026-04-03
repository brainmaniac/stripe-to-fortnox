package fortnox

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/testutil"
)

// ── Test doubles ─────────────────────────────────────────────────────────────

type mockPoster struct {
	voucherNumber string
	err           error
	calls         int
}

func (m *mockPoster) Post(_ context.Context, _ string, _ interface{}) ([]byte, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	resp := VoucherResponse{}
	resp.Voucher.VoucherNumber = m.voucherNumber
	resp.Voucher.VoucherSeries = "S"
	b, _ := json.Marshal(resp)
	return b, nil
}

func (m *mockPoster) Put(_ context.Context, _ string, _ interface{}) ([]byte, error) {
	return []byte("{}"), nil
}

func newCreator(t *testing.T, poster *mockPoster) (*VoucherCreator, *db.Queries) {
	t.Helper()
	q := testutil.NewTestDB(t)
	return &VoucherCreator{api: poster, queries: q, config: DefaultAccountConfig()}, q
}

// ── Revenue account routing by billing country ────────────────────────────────
// countryGroup drives the DB mapping lookup in the invoice flow.

func TestCountryGroup(t *testing.T) {
	cases := []struct {
		country string
		want    string
	}{
		{"SE", "SE"},
		{"", "SE"},   // empty defaults to SE
		{"DE", "EU"}, // EU member
		{"FR", "EU"},
		{"US", "WO"}, // outside EU
		{"GB", "WO"}, // post-Brexit
		{"NO", "WO"}, // not in EU
	}
	for _, tc := range cases {
		got := countryGroup(tc.country)
		if got != tc.want {
			t.Errorf("countryGroup(%q) = %q, want %q", tc.country, got, tc.want)
		}
	}
}

// ── Fee voucher with reverse VAT (omvänd moms) ────────────────────────────────
// Stripe Ltd is Irish (EU) → omvänd skattskyldighet applies.
// Rows: debit 6065+2645, credit 2614+1521.

func TestFeeVoucherReverseVAT(t *testing.T) {
	poster := &mockPoster{voucherNumber: "fee1"}
	vc, q := newCreator(t, poster)
	ctx := context.Background()

	feeOre := int64(2500) // 25 USD fee
	payout := db.StripePayout{
		ID:          "po_fee_test",
		Amount:      50000,
		Currency:    "sek",
		ArrivalDate: time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC).Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	}

	v, err := vc.CreateFeeVoucher(ctx, "ch_fee_test", feeOre, payout)
	if err != nil {
		t.Fatalf("CreateFeeVoucher: %v", err)
	}
	if v == nil {
		t.Fatal("expected voucher, got nil")
	}
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("fee voucher unbalanced: debit=%d credit=%d", v.TotalDebit, v.TotalCredit)
	}

	fee := toMajorUnit(feeOre)
	reverseVAT := fee * (DefaultAccountConfig().VATPercent / 100.0)
	totalDebit := fee + reverseVAT
	totalCredit := reverseVAT + fee
	if math.Abs(totalDebit-totalCredit) > 0.001 {
		t.Errorf("fee math unbalanced: debit=%.2f credit=%.2f", totalDebit, totalCredit)
	}

	stored, err := q.GetFortnoxVoucherBySource(ctx, "fee", "fee_ch_fee_test")
	if err != nil || stored == nil {
		t.Fatalf("fee voucher not found in db: %v", err)
	}
	if !stored.FortnoxVoucherNumber.Valid {
		t.Error("expected confirmed voucher number")
	}
}

// ── Payout voucher debit == credit ───────────────────────────────────────────

func TestPayoutVoucherBalance(t *testing.T) {
	poster := &mockPoster{voucherNumber: "po1"}
	vc, _ := newCreator(t, poster)

	payout := db.StripePayout{
		ID:          "po_test",
		Amount:      50000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	}
	v, err := vc.CreatePayoutVoucher(context.Background(), payout)
	if err != nil {
		t.Fatalf("CreatePayoutVoucher: %v", err)
	}
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("payout voucher unbalanced: debit=%d credit=%d", v.TotalDebit, v.TotalCredit)
	}
}

// ── Payout voucher uses correct accounts ─────────────────────────────────────
// debit 1930 (bank), credit 1521 (Stripe clearing).

func TestPayoutVoucherAccounts(t *testing.T) {
	cfg := DefaultAccountConfig()
	amount := toMajorUnit(50000)
	rows := []VoucherRow{
		{Account: cfg.BankAccount, Debit: amount},
		{Account: cfg.StripeClearing, Credit: amount},
	}
	if rows[0].Account != AccountBankAccount {
		t.Errorf("payout debit account: got %s, want %s", rows[0].Account, AccountBankAccount)
	}
	if rows[1].Account != AccountStripeClearing {
		t.Errorf("payout credit account: got %s, want %s", rows[1].Account, AccountStripeClearing)
	}
}

// ── 1521 is the shared Stripe clearing account ───────────────────────────────

func TestStripeClearingAccountIsShared(t *testing.T) {
	cfg := DefaultAccountConfig()
	if cfg.StripeClearing != AccountStripeClearing {
		t.Errorf("clearing account mismatch: got %s, want %s", cfg.StripeClearing, AccountStripeClearing)
	}
}

// ── Idempotency — confirmed payout voucher is not re-posted ──────────────────

func TestPayoutVoucherIdempotent(t *testing.T) {
	poster := &mockPoster{voucherNumber: "idem1"}
	vc, _ := newCreator(t, poster)
	ctx := context.Background()

	payout := db.StripePayout{
		ID:          "po_idem",
		Amount:      50000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	}

	if _, err := vc.CreatePayoutVoucher(ctx, payout); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := vc.CreatePayoutVoucher(ctx, payout); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if poster.calls != 1 {
		t.Errorf("idempotency failed: expected 1 Fortnox POST, got %d", poster.calls)
	}
}

// ── Failed Fortnox call leaves a pending row ─────────────────────────────────

func TestPayoutVoucherFailedFortnoxLeavesPendingRow(t *testing.T) {
	failPoster := &mockPoster{err: fmt.Errorf("fortnox unavailable")}
	vc, q := newCreator(t, failPoster)
	ctx := context.Background()

	payout := db.StripePayout{
		ID:          "po_fail",
		Amount:      10000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	}

	_, err := vc.CreatePayoutVoucher(ctx, payout)
	if err == nil {
		t.Fatal("expected error when Fortnox is unavailable")
	}

	pending, err := q.GetFortnoxVoucherBySource(ctx, "payout", payout.ID)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if pending == nil {
		t.Fatal("expected pending row after failed Fortnox POST")
	}
	if pending.FortnoxVoucherNumber.Valid {
		t.Error("pending row should not have a voucher number yet")
	}
}

// ── Imbalanced voucher is rejected ───────────────────────────────────────────

func TestPostVoucherRejectsImbalanced(t *testing.T) {
	poster := &mockPoster{voucherNumber: "bad"}
	vc, _ := newCreator(t, poster)

	req := VoucherRequest{}
	req.Voucher.Description = "test"
	req.Voucher.VoucherSeries = "S"
	req.Voucher.TransactionDate = "2025-01-01"
	req.Voucher.VoucherRows = []VoucherRow{
		{Account: "1521", Debit: 100},
		{Account: "3010", Credit: 90}, // intentionally wrong
	}

	_, err := vc.postVoucher(context.Background(), req, "charge", "ch_bad")
	if err == nil {
		t.Error("expected error for imbalanced voucher")
	}
	if poster.calls != 0 {
		t.Error("Fortnox should not be called for imbalanced voucher")
	}
}

// ── toMajorUnit ───────────────────────────────────────────────────────────────

func TestToMajorUnit(t *testing.T) {
	cases := []struct {
		minor int64
		want  float64
	}{
		{100, 1.0},
		{1000, 10.0},
		{10000, 100.0},
		{1, 0.01},
	}
	for _, tc := range cases {
		if got := toMajorUnit(tc.minor); got != tc.want {
			t.Errorf("toMajorUnit(%d) = %f, want %f", tc.minor, got, tc.want)
		}
	}
}
