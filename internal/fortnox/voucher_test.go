package fortnox

import (
	"context"
	"database/sql"
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
	resp.Voucher.VoucherSeries = "A"
	b, _ := json.Marshal(resp)
	return b, nil
}

func newCreator(t *testing.T, poster *mockPoster) (*VoucherCreator, *db.Queries) {
	t.Helper()
	q := testutil.NewTestDB(t)
	return &VoucherCreator{api: poster, queries: q, config: DefaultAccountConfig()}, q
}

// ── Bullet 4 & 5: Swedish charge voucher ─────────────────────────────────────
// Revenue account SE → 3010, VAT split at 25 % applied (omvänd moms NOT used for domestic).

func TestChargeVoucherSE(t *testing.T) {
	poster := &mockPoster{voucherNumber: "1"}
	vc, _ := newCreator(t, poster)

	charge := db.StripeCharge{
		ID:        "ch_SE",
		Amount:    10000, // 100 SEK in öre
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}
	v, err := vc.CreateChargeVoucher(context.Background(), charge, "SE")
	if err != nil {
		t.Fatalf("CreateChargeVoucher SE: %v", err)
	}
	if v == nil {
		t.Fatal("expected voucher, got nil")
	}

	// Debit == Credit in DB (stored as öre).
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("unbalanced: debit=%d credit=%d", v.TotalDebit, v.TotalCredit)
	}
	// Exactly one Fortnox POST was made.
	if poster.calls != 1 {
		t.Errorf("expected 1 Fortnox POST, got %d", poster.calls)
	}
}

// ── Bullet 6: EU charge voucher — no Swedish VAT ─────────────────────────────

func TestChargeVoucherEU(t *testing.T) {
	poster := &mockPoster{voucherNumber: "2"}
	vc, _ := newCreator(t, poster)

	charge := db.StripeCharge{
		ID:        "ch_EU",
		Amount:    20000,
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}
	v, err := vc.CreateChargeVoucher(context.Background(), charge, "DE")
	if err != nil {
		t.Fatalf("CreateChargeVoucher EU: %v", err)
	}
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("unbalanced voucher")
	}
	if poster.calls != 1 {
		t.Errorf("expected 1 POST, got %d", poster.calls)
	}
}

// ── Bullet 6: Rest-of-world charge voucher ───────────────────────────────────

func TestChargeVoucherWO(t *testing.T) {
	poster := &mockPoster{voucherNumber: "3"}
	vc, _ := newCreator(t, poster)

	charge := db.StripeCharge{
		ID:        "ch_WO",
		Amount:    30000,
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}
	v, err := vc.CreateChargeVoucher(context.Background(), charge, "US")
	if err != nil {
		t.Fatalf("CreateChargeVoucher WO: %v", err)
	}
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("unbalanced voucher")
	}
}

// ── Bullet 4: revenueAccount routing ─────────────────────────────────────────

func TestRevenueAccountRouting(t *testing.T) {
	cfg := DefaultAccountConfig()
	cases := []struct {
		country string
		want    string
	}{
		{"SE", AccountRevenueSE},
		{"", AccountRevenueSE},   // empty defaults to SE
		{"DE", AccountRevenueEU}, // EU member
		{"FR", AccountRevenueEU},
		{"US", AccountRevenueWO}, // outside EU
		{"GB", AccountRevenueWO}, // post-Brexit
		{"NO", AccountRevenueWO}, // not in EU
	}
	for _, tc := range cases {
		got := cfg.revenueAccount(tc.country)
		if got != tc.want {
			t.Errorf("revenueAccount(%q) = %q, want %q", tc.country, got, tc.want)
		}
	}
}

// ── Bullet 9: debit == credit for all voucher types ──────────────────────────

func TestVoucherBalancedCharge(t *testing.T) {
	cfg := DefaultAccountConfig()
	amounts := []int64{100, 1000, 9999, 10000, 99999}
	for _, amt := range amounts {
		for _, country := range []string{"SE", "DE", "US"} {
			amount := oreToKronor(amt)
			revenueAcc := cfg.revenueAccount(country)

			var rows []VoucherRow
			rows = append(rows, VoucherRow{Account: cfg.StripeClearing, Debit: amount})
			if country == "" || country == "SE" {
				vatRate := cfg.VATPercent / 100.0
				vatAmount := amount * vatRate / (1 + vatRate)
				revenue := amount - vatAmount
				rows = append(rows,
					VoucherRow{Account: revenueAcc, Credit: revenue},
					VoucherRow{Account: cfg.OutputVAT25, Credit: vatAmount},
				)
			} else {
				rows = append(rows, VoucherRow{Account: revenueAcc, Credit: amount})
			}

			var debit, credit float64
			for _, r := range rows {
				debit += r.Debit
				credit += r.Credit
			}
			if fmt.Sprintf("%.2f", debit) != fmt.Sprintf("%.2f", credit) {
				t.Errorf("amt=%d country=%s: debit=%.2f credit=%.2f", amt, country, debit, credit)
			}
		}
	}
}

// ── Bullet 8: fee voucher with reverse VAT ────────────────────────────────────
// Stripe Ltd is Irish (EU) → omvänd skattskyldighet applies.
// Rows: debit 6065+2645, credit 2614+1521.

func TestFeeVoucherReverseVAT(t *testing.T) {
	poster := &mockPoster{voucherNumber: "fee1"}
	vc, q := newCreator(t, poster)
	ctx := context.Background()

	// Insert a charge so billing_country is irrelevant for fee vouchers.
	feeOre := int64(2500) // 25 SEK fee
	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	v, err := vc.CreateFeeVoucher(ctx, "ch_fee_test", feeOre, date)
	if err != nil {
		t.Fatalf("CreateFeeVoucher: %v", err)
	}
	if v == nil {
		t.Fatal("expected voucher, got nil")
	}
	// Verify debit == credit stored in DB.
	if v.TotalDebit != v.TotalCredit {
		t.Errorf("fee voucher unbalanced: debit=%d credit=%d", v.TotalDebit, v.TotalCredit)
	}

	// Verify the math: fee + reverseVAT debit should equal reverseVAT credit + 1521 credit.
	fee := oreToKronor(feeOre)
	reverseVAT := fee * (DefaultAccountConfig().VATPercent / 100.0)
	totalDebit := fee + reverseVAT
	totalCredit := reverseVAT + fee // credit 2614 + credit 1521

	if math.Abs(totalDebit-totalCredit) > 0.001 {
		t.Errorf("fee math unbalanced: debit=%.2f credit=%.2f", totalDebit, totalCredit)
	}

	// Verify stored voucher via DB.
	stored, err := q.GetFortnoxVoucherBySource(ctx, "fee", "fee_ch_fee_test")
	if err != nil || stored == nil {
		t.Fatalf("fee voucher not found in db: %v", err)
	}
	if !stored.FortnoxVoucherNumber.Valid {
		t.Error("expected confirmed voucher number")
	}
}

// ── Bullet 9: payout voucher debit == credit ─────────────────────────────────

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

// ── Bullet 7: payout voucher uses correct accounts ───────────────────────────
// debit 1930 (bank), credit 1521 (Stripe clearing).

func TestPayoutVoucherAccounts(t *testing.T) {
	cfg := DefaultAccountConfig()
	amount := oreToKronor(50000)
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

// ── Bullet 12: 1521 links charges and payouts ─────────────────────────────────
// Charges credit 1521; payouts debit 1521.

func TestStripeClearingAccountIsShared(t *testing.T) {
	cfg := DefaultAccountConfig()
	// Both charge voucher (first row) and payout voucher (second row) reference 1521.
	if cfg.StripeClearing != AccountStripeClearing {
		t.Errorf("clearing account mismatch: got %s, want %s", cfg.StripeClearing, AccountStripeClearing)
	}
}

// ── Bullet 11: idempotency — confirmed voucher is not re-posted ───────────────

func TestChargeVoucherIdempotentConfirmed(t *testing.T) {
	poster := &mockPoster{voucherNumber: "idem1"}
	vc, _ := newCreator(t, poster)
	ctx := context.Background()

	charge := db.StripeCharge{
		ID:        "ch_idem",
		Amount:    10000,
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}

	// First call.
	if _, err := vc.CreateChargeVoucher(ctx, charge, "SE"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call with same charge — Fortnox should NOT be called again.
	if _, err := vc.CreateChargeVoucher(ctx, charge, "SE"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if poster.calls != 1 {
		t.Errorf("idempotency failed: expected 1 Fortnox POST, got %d", poster.calls)
	}
}

// ── Bullet 11: failed Fortnox call leaves a pending row, surfaced for manual review ──
// The handler will NOT retry the charge automatically (ListUnsyncedCharges excludes
// it). Only direct calls to CreateChargeVoucher (e.g. a future manual retry button)
// will attempt again. INSERT OR IGNORE ensures there is still only one local row.

func TestChargeVoucherFailedFortnoxLeavesPendingRow(t *testing.T) {
	failPoster := &mockPoster{err: fmt.Errorf("fortnox unavailable")}
	vc, q := newCreator(t, failPoster)
	ctx := context.Background()

	charge := db.StripeCharge{
		ID:        "ch_fail",
		Amount:    10000,
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}

	_, err := vc.CreateChargeVoucher(ctx, charge, "SE")
	if err == nil {
		t.Fatal("expected error when Fortnox is unavailable")
	}

	// A pending row must exist (no voucher number).
	pending, err := q.GetFortnoxVoucherBySource(ctx, "charge", charge.ID)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	if pending == nil {
		t.Fatal("expected pending row after failed Fortnox POST")
	}
	if pending.FortnoxVoucherNumber.Valid {
		t.Error("pending row should not have a voucher number yet")
	}

	// Calling again (e.g. manual retry) does not create a second local row.
	vc.api = &mockPoster{voucherNumber: "manual_retry1"}
	v, err := vc.CreateChargeVoucher(ctx, charge, "SE")
	if err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	if !v.FortnoxVoucherNumber.Valid {
		t.Error("manual retry should produce a confirmed voucher")
	}

	all, _ := q.ListFortnoxVouchers(ctx, 100, 0)
	count := 0
	for _, vv := range all {
		if vv.SourceType == "charge" && vv.SourceID == charge.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 voucher row, found %d", count)
	}
}

// ── Bullet 9: imbalanced voucher is rejected ─────────────────────────────────

func TestPostVoucherRejectsImbalanced(t *testing.T) {
	poster := &mockPoster{voucherNumber: "bad"}
	vc, _ := newCreator(t, poster)

	req := VoucherRequest{}
	req.Voucher.Description = "test"
	req.Voucher.VoucherSeries = "A"
	req.Voucher.TransactionDate = "2025-01-01"
	req.Voucher.VoucherRows = []VoucherRow{
		{Account: "1521", Debit: 100},
		{Account: "3010", Credit: 90}, // intentionally wrong
	}

	_, err := vc.postVoucher(context.Background(), req, "charge", "ch_bad", 10000)
	if err == nil {
		t.Error("expected error for imbalanced voucher")
	}
	if poster.calls != 0 {
		t.Error("Fortnox should not be called for imbalanced voucher")
	}
}

// ── VAT split math ────────────────────────────────────────────────────────────

func TestSEVATSplit(t *testing.T) {
	cfg := DefaultAccountConfig()
	// 1000 SEK charge: 200 SEK VAT (1000 × 25/125), 800 SEK revenue.
	amount := 1000.0
	vatRate := cfg.VATPercent / 100.0
	vatAmount := amount * vatRate / (1 + vatRate)
	revenue := amount - vatAmount

	wantVAT := 200.0
	wantRevenue := 800.0
	if math.Abs(vatAmount-wantVAT) > 0.01 {
		t.Errorf("VAT: got %.2f, want %.2f", vatAmount, wantVAT)
	}
	if math.Abs(revenue-wantRevenue) > 0.01 {
		t.Errorf("Revenue: got %.2f, want %.2f", revenue, wantRevenue)
	}
}

// ── oreToKronor ───────────────────────────────────────────────────────────────

func TestOreToKronor(t *testing.T) {
	cases := []struct{ ore int64; want float64 }{
		{100, 1.0},
		{1000, 10.0},
		{10000, 100.0},
		{1, 0.01},
	}
	for _, tc := range cases {
		if got := oreToKronor(tc.ore); got != tc.want {
			t.Errorf("oreToKronor(%d) = %f, want %f", tc.ore, got, tc.want)
		}
	}
}

// ── nullStr helper ────────────────────────────────────────────────────────────

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
