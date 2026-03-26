package db_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/testutil"
)

// ── Bullet 10: Stripe upserts are idempotent ──────────────────────────────────

func TestUpsertStripeChargeIdempotent(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	charge := db.StripeCharge{
		ID:        "ch_idem",
		Amount:    10000,
		Currency:  "sek",
		Status:    "succeeded",
		CreatedAt: time.Now().Unix(),
	}

	if err := q.UpsertStripeCharge(ctx, charge); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := q.UpsertStripeCharge(ctx, charge); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	count, err := q.CountStripeCharges(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 charge, got %d", count)
	}
}

func TestUpsertStripePayoutIdempotent(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	payout := db.StripePayout{
		ID:          "po_idem",
		Amount:      50000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	}

	for i := 0; i < 3; i++ {
		if err := q.UpsertStripePayout(ctx, payout); err != nil {
			t.Fatalf("upsert #%d: %v", i+1, err)
		}
	}

	count, err := q.CountStripePayouts(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 payout, got %d", count)
	}
}

func TestUpsertStripeCustomerIdempotent(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	if err := q.UpsertStripeCustomer(ctx, "cus_1", "a@b.com", "Alice", "SE", 1000); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Same ID, updated email.
	if err := q.UpsertStripeCustomer(ctx, "cus_1", "new@b.com", "Alice", "SE", 1000); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	count, _ := q.CountStripeCustomers(ctx)
	if count != 1 {
		t.Errorf("expected 1 customer, got %d", count)
	}
	// Email should be updated to the latest value.
	c, err := q.GetStripeCustomer(ctx, "cus_1")
	if err != nil || c == nil {
		t.Fatalf("get customer: %v", err)
	}
	if c.Email.String != "new@b.com" {
		t.Errorf("email not updated: got %q", c.Email.String)
	}
}

func TestUpsertStripeBalanceTransactionIdempotent(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	// Insert a payout first (FK constraint).
	_ = q.UpsertStripePayout(ctx, db.StripePayout{
		ID:          "po_bt",
		Amount:      10000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	})

	bt := db.StripeBalanceTransaction{
		ID:          "txn_1",
		Amount:      10000,
		Fee:         200,
		Net:         9800,
		Currency:    "sek",
		Type:        "charge",
		PayoutID:    sql.NullString{String: "po_bt", Valid: true},
		CreatedAt:   time.Now().Unix(),
		AvailableOn: time.Now().Unix(),
	}

	for i := 0; i < 2; i++ {
		if err := q.UpsertStripeBalanceTransaction(ctx, bt); err != nil {
			t.Fatalf("upsert #%d: %v", i+1, err)
		}
	}

	txns, err := q.ListBalanceTransactionsForPayout(ctx, "po_bt")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(txns) != 1 {
		t.Errorf("expected 1 balance txn, got %d", len(txns))
	}
}

// ── Bullet 10: billing_country COALESCE — existing value not overwritten by NULL ──

func TestUpsertChargePreservesBillingCountry(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	// First insert with a billing country.
	charge := db.StripeCharge{
		ID:             "ch_country",
		Amount:         10000,
		Currency:       "sek",
		Status:         "succeeded",
		CreatedAt:      time.Now().Unix(),
		BillingCountry: sql.NullString{String: "DE", Valid: true},
	}
	if err := q.UpsertStripeCharge(ctx, charge); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Update without billing country (e.g. webhook missing billing field).
	charge.BillingCountry = sql.NullString{}
	if err := q.UpsertStripeCharge(ctx, charge); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := q.GetStripeCharge(ctx, "ch_country")
	if err != nil || got == nil {
		t.Fatalf("get charge: %v", err)
	}
	if got.BillingCountry.String != "DE" {
		t.Errorf("billing_country should be preserved; got %q", got.BillingCountry.String)
	}
}

// ── Bullet 11: ListUnsyncedCharges includes pending, excludes confirmed ────────

func TestListUnsyncedChargesPendingAndConfirmed(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	// Insert two succeeded charges.
	for _, id := range []string{"ch_a", "ch_b"} {
		_ = q.UpsertStripeCharge(ctx, db.StripeCharge{
			ID:        id,
			Amount:    10000,
			Currency:  "sek",
			Status:    "succeeded",
			CreatedAt: time.Now().Unix(),
		})
	}

	// Both should be unsynced.
	charges, err := q.ListUnsyncedCharges(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(charges) != 2 {
		t.Fatalf("expected 2 unsynced, got %d", len(charges))
	}

	// Insert a pending voucher for ch_a (no voucher number yet).
	_ = q.InsertPendingFortnoxVoucher(ctx, db.FortnoxVoucher{
		FortnoxVoucherSeries: "A",
		VoucherDate:          "2025-01-01",
		SourceType:           "charge",
		SourceID:             "ch_a",
		TotalDebit:           10000,
		TotalCredit:          10000,
	})

	// ch_a should no longer appear — a pending row blocks automatic retry.
	charges, _ = q.ListUnsyncedCharges(ctx)
	if len(charges) != 1 {
		t.Errorf("pending row should remove charge from unsynced list; got %d", len(charges))
	}
	if charges[0].ID != "ch_b" {
		t.Errorf("expected ch_b, got %s", charges[0].ID)
	}

	// Confirm ch_a.
	_ = q.ConfirmFortnoxVoucher(ctx, "V1", "{}", "charge", "ch_a")

	// ch_a is still gone after confirm — confirmed also blocks unsynced list.
	charges, _ = q.ListUnsyncedCharges(ctx)
	if len(charges) != 1 {
		t.Errorf("after confirm expected 1 unsynced, got %d", len(charges))
	}
	if charges[0].ID != "ch_b" {
		t.Errorf("expected ch_b remaining, got %s", charges[0].ID)
	}
}

func TestListUnsyncedPayoutsPendingAndConfirmed(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	for _, id := range []string{"po_x", "po_y"} {
		_ = q.UpsertStripePayout(ctx, db.StripePayout{
			ID:          id,
			Amount:      50000,
			Currency:    "sek",
			ArrivalDate: time.Now().Unix(),
			Status:      "paid",
			CreatedAt:   time.Now().Unix(),
		})
	}

	payouts, _ := q.ListUnsyncedPayouts(ctx)
	if len(payouts) != 2 {
		t.Fatalf("expected 2 unsynced payouts, got %d", len(payouts))
	}

	// Pending voucher for po_x.
	_ = q.InsertPendingFortnoxVoucher(ctx, db.FortnoxVoucher{
		FortnoxVoucherSeries: "A",
		VoucherDate:          "2025-01-01",
		SourceType:           "payout",
		SourceID:             "po_x",
		TotalDebit:           50000,
		TotalCredit:          50000,
	})

	// Pending row removes po_x from the unsynced list — it won't be auto-retried.
	payouts, _ = q.ListUnsyncedPayouts(ctx)
	if len(payouts) != 1 {
		t.Errorf("pending payout should be removed from unsynced list; got %d", len(payouts))
	}

	// Confirm po_x.
	_ = q.ConfirmFortnoxVoucher(ctx, "V2", "{}", "payout", "po_x")

	payouts, _ = q.ListUnsyncedPayouts(ctx)
	if len(payouts) != 1 {
		t.Errorf("expected 1 unsynced payout, got %d", len(payouts))
	}
}

// ── Bullet 11: INSERT OR IGNORE prevents duplicate pending rows ───────────────

func TestInsertPendingVoucherIdempotent(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	pending := db.FortnoxVoucher{
		FortnoxVoucherSeries: "A",
		VoucherDate:          "2025-01-01",
		SourceType:           "charge",
		SourceID:             "ch_pending",
		TotalDebit:           10000,
		TotalCredit:          10000,
	}

	// Insert twice — second should silently no-op.
	if err := q.InsertPendingFortnoxVoucher(ctx, pending); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := q.InsertPendingFortnoxVoucher(ctx, pending); err != nil {
		t.Fatalf("second insert should silently skip: %v", err)
	}

	all, _ := q.ListFortnoxVouchers(ctx, 100, 0)
	count := 0
	for _, v := range all {
		if v.SourceType == "charge" && v.SourceID == "ch_pending" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 pending row, got %d", count)
	}
}

// ── Bullet 2: balance transactions stored and queryable per payout ────────────

func TestBalanceTransactionsPerPayout(t *testing.T) {
	q := testutil.NewTestDB(t)
	ctx := context.Background()

	_ = q.UpsertStripePayout(ctx, db.StripePayout{
		ID:          "po_multi",
		Amount:      20000,
		Currency:    "sek",
		ArrivalDate: time.Now().Unix(),
		Status:      "paid",
		CreatedAt:   time.Now().Unix(),
	})

	for i, id := range []string{"txn_a", "txn_b", "txn_c"} {
		_ = q.UpsertStripeBalanceTransaction(ctx, db.StripeBalanceTransaction{
			ID:          id,
			Amount:      int64(i+1) * 1000,
			Fee:         50,
			Net:         int64(i+1)*1000 - 50,
			Currency:    "sek",
			Type:        "charge",
			PayoutID:    sql.NullString{String: "po_multi", Valid: true},
			CreatedAt:   time.Now().Unix(),
			AvailableOn: time.Now().Unix(),
		})
	}

	txns, err := q.ListBalanceTransactionsForPayout(ctx, "po_multi")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(txns) != 3 {
		t.Errorf("expected 3 txns for payout, got %d", len(txns))
	}
}
