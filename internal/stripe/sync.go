package stripe

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	stripelib "github.com/stripe/stripe-go/v84"
	"github.com/stripe/stripe-go/v84/client"
	"stripe-fortnox-sync/internal/db"
)

// Syncer pulls data from Stripe and stores it in the local database.
type Syncer struct {
	api     *client.API
	queries *db.Queries
}

func NewSyncer(apiKey string, queries *db.Queries) *Syncer {
	sc := &client.API{}
	sc.Init(apiKey, nil)
	return &Syncer{api: sc, queries: queries}
}

// SyncAll runs all sync operations in sequence, logging (but not aborting on) errors.
func (s *Syncer) SyncAll(ctx context.Context) error {
	if err := s.SyncCustomers(ctx); err != nil {
		log.Printf("sync customers error: %v", err)
	}
	if err := s.SyncCharges(ctx); err != nil {
		log.Printf("sync charges error: %v", err)
	}
	if err := s.SyncPayouts(ctx); err != nil {
		log.Printf("sync payouts error: %v", err)
	}
	return nil
}

// SyncCustomers fetches customers from Stripe and upserts them locally.
// Uses created[gte] for incremental syncs based on last_synced_at.
func (s *Syncer) SyncCustomers(ctx context.Context) error {
	state, err := s.queries.GetSyncState(ctx, "customers")
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}
	if err := s.queries.SetSyncStatus(ctx, "running", "customers"); err != nil {
		return err
	}

	// Record the start time before we pull — ensures we don't miss objects
	// created during the sync window on the next run.
	syncStarted := time.Now().Unix()

	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "customers") }()

	params := &stripelib.CustomerListParams{}
	params.Filters.AddFilter("limit", "", "100")
	if state != nil && state.LastSyncedAt.Valid && state.LastSyncedAt.Int64 > 0 {
		// 60-second buffer to handle clock skew and objects created at the boundary.
		ts := state.LastSyncedAt.Int64 - 60
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(ts, 10))
	}

	iter := s.api.Customers.List(params)
	var lastID string
	count := 0
	for iter.Next() {
		c := iter.Customer()
		country := ""
		if c.Address != nil {
			country = c.Address.Country
		}
		if err := s.queries.UpsertStripeCustomer(ctx, c.ID, c.Email, c.Name, country, c.Created); err != nil {
			log.Printf("upsert customer %s: %v", c.ID, err)
			continue
		}
		_ = s.queries.InsertSyncLog(ctx, "customers", c.ID, "synced_from_stripe", "")
		lastID = c.ID
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "customers")
		return fmt.Errorf("list customers: %w", err)
	}
	_ = s.queries.UpdateSyncState(ctx, lastID, syncStarted, "idle", "customers")
	log.Printf("synced %d customers", count)
	return nil
}

// SyncCharges fetches charges from Stripe and upserts them locally.
// Uses created[gte] for incremental syncs.
func (s *Syncer) SyncCharges(ctx context.Context) error {
	state, err := s.queries.GetSyncState(ctx, "charges")
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}
	if err := s.queries.SetSyncStatus(ctx, "running", "charges"); err != nil {
		return err
	}
	syncStarted := time.Now().Unix()
	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "charges") }()

	params := &stripelib.ChargeListParams{}
	params.Filters.AddFilter("limit", "", "100")
	if state != nil && state.LastSyncedAt.Valid && state.LastSyncedAt.Int64 > 0 {
		ts := state.LastSyncedAt.Int64 - 60
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(ts, 10))
	}

	iter := s.api.Charges.List(params)
	var lastID string
	count := 0
	for iter.Next() {
		c := iter.Charge()
		charge := chargeFromStripe(c)
		if err := s.queries.UpsertStripeCharge(ctx, charge); err != nil {
			log.Printf("upsert charge %s: %v", c.ID, err)
			continue
		}
		_ = s.queries.InsertSyncLog(ctx, "charges", c.ID, "synced_from_stripe", "")
		lastID = c.ID
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "charges")
		return fmt.Errorf("list charges: %w", err)
	}
	_ = s.queries.UpdateSyncState(ctx, lastID, syncStarted, "idle", "charges")
	log.Printf("synced %d charges", count)
	return nil
}

// SyncPayouts fetches payouts and triggers balance-transaction sync for paid ones.
func (s *Syncer) SyncPayouts(ctx context.Context) error {
	state, err := s.queries.GetSyncState(ctx, "payouts")
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}
	if err := s.queries.SetSyncStatus(ctx, "running", "payouts"); err != nil {
		return err
	}
	syncStarted := time.Now().Unix()
	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "payouts") }()

	params := &stripelib.PayoutListParams{}
	params.Filters.AddFilter("limit", "", "100")
	if state != nil && state.LastSyncedAt.Valid && state.LastSyncedAt.Int64 > 0 {
		ts := state.LastSyncedAt.Int64 - 60
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(ts, 10))
	}

	iter := s.api.Payouts.List(params)
	var lastID string
	count := 0
	for iter.Next() {
		p := iter.Payout()
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
		if err := s.queries.UpsertStripePayout(ctx, payout); err != nil {
			log.Printf("upsert payout %s: %v", p.ID, err)
			continue
		}
		if string(p.Status) == "paid" {
			go func(id string) {
				if err := s.SyncBalanceTransactionsForPayout(context.Background(), id); err != nil {
					log.Printf("sync balance txns for payout %s: %v", id, err)
				}
			}(p.ID)
		}
		_ = s.queries.InsertSyncLog(ctx, "payouts", p.ID, "synced_from_stripe", "")
		lastID = p.ID
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "payouts")
		return fmt.Errorf("list payouts: %w", err)
	}
	_ = s.queries.UpdateSyncState(ctx, lastID, syncStarted, "idle", "payouts")
	log.Printf("synced %d payouts", count)
	return nil
}

// SyncBalanceTransactionsForPayout fetches all balance transactions for a payout.
// The payout field on BalanceTransactionListParams filters by payout ID, returning
// only the transactions settled in that specific payout.
func (s *Syncer) SyncBalanceTransactionsForPayout(ctx context.Context, payoutID string) error {
	params := &stripelib.BalanceTransactionListParams{
		Payout: stripelib.String(payoutID),
	}
	params.Filters.AddFilter("limit", "", "100")

	iter := s.api.BalanceTransactions.List(params)
	for iter.Next() {
		bt := iter.BalanceTransaction()
		record := db.StripeBalanceTransaction{
			ID:          bt.ID,
			Amount:      bt.Amount,
			Fee:         bt.Fee,
			Net:         bt.Net,
			Currency:    string(bt.Currency),
			Type:        string(bt.Type),
			CreatedAt:   bt.Created,
			AvailableOn: bt.AvailableOn,
			PayoutID:    sql.NullString{String: payoutID, Valid: true},
		}
		if bt.Source != nil && bt.Source.ID != "" {
			record.SourceID = sql.NullString{String: bt.Source.ID, Valid: true}
		}
		if bt.Description != "" {
			record.Description = sql.NullString{String: bt.Description, Valid: true}
		}
		if err := s.queries.UpsertStripeBalanceTransaction(ctx, record); err != nil {
			log.Printf("upsert balance txn %s: %v", bt.ID, err)
		}
	}
	return iter.Err()
}

// chargeFromStripe maps a Stripe charge object to the local db model.
func chargeFromStripe(c *stripelib.Charge) db.StripeCharge {
	charge := db.StripeCharge{
		ID:             c.ID,
		Amount:         c.Amount,
		AmountCaptured: c.AmountCaptured,
		Currency:       string(c.Currency),
		Status:         string(c.Status),
		CreatedAt:      c.Created,
	}
	if c.BalanceTransaction != nil && c.BalanceTransaction.ID != "" {
		charge.BalanceTransactionID = sql.NullString{String: c.BalanceTransaction.ID, Valid: true}
	}
	if c.Customer != nil && c.Customer.ID != "" {
		charge.CustomerID = sql.NullString{String: c.Customer.ID, Valid: true}
	}
	if c.PaymentIntent != nil && c.PaymentIntent.ID != "" {
		charge.PaymentIntentID = sql.NullString{String: c.PaymentIntent.ID, Valid: true}
	}
	if c.Description != "" {
		charge.Description = sql.NullString{String: c.Description, Valid: true}
	}
	// Capture billing country for SE/EU/WO revenue account routing.
	if c.BillingDetails != nil && c.BillingDetails.Address != nil && c.BillingDetails.Address.Country != "" {
		charge.BillingCountry = sql.NullString{String: c.BillingDetails.Address.Country, Valid: true}
	}
	return charge
}
