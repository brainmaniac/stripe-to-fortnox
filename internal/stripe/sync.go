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

// syncFromTimestamp returns the configured charges_sync_from_date as a Unix timestamp,
// falling back to the start of the current year if no setting is stored.
func (s *Syncer) syncFromTimestamp(ctx context.Context) int64 {
	setting, err := s.queries.GetSetting(ctx, "charges_sync_from_date")
	if err == nil && setting != nil && setting.Value != "" {
		if t, err := time.Parse("2006-01-02", setting.Value); err == nil {
			return t.UTC().Unix()
		}
	}
	return startOfYear()
}

// startOfYear returns the Unix timestamp for Jan 1 of the current year (UTC).
func startOfYear() int64 {
	now := time.Now().UTC()
	return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Unix()
}

// startOfLastYear returns the Unix timestamp for Jan 1 of last year (UTC).
func startOfLastYear() int64 {
	now := time.Now().UTC()
	return time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
}

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
// Uses MAX(created_at) from the local table as the incremental cursor.
func (s *Syncer) SyncCustomers(ctx context.Context) error {
	if err := s.queries.SetSyncStatus(ctx, "running", "customers"); err != nil {
		return err
	}
	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "customers") }()

	params := &stripelib.CustomerListParams{}
	params.Filters.AddFilter("limit", "", "100")
	maxAt, _ := s.queries.MaxCustomerCreatedAt(ctx)
	if maxAt > 0 {
		// Incremental: only fetch customers created since the last known one.
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(maxAt-10, 10))
	} else {
		// First run: use the same configured start date as charges/payouts.
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(s.syncFromTimestamp(ctx), 10))
	}

	iter := s.api.Customers.List(params)
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
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "customers")
		return fmt.Errorf("list customers: %w", err)
	}
	_ = s.queries.SetSyncStatus(ctx, "idle", "customers")
	log.Printf("synced %d customers", count)
	return nil
}

// SyncCharges fetches charges from Stripe and upserts them locally.
// Uses MAX(created_at) from the local table as the incremental cursor.
func (s *Syncer) SyncCharges(ctx context.Context) error {
	if err := s.queries.SetSyncStatus(ctx, "running", "charges"); err != nil {
		return err
	}
	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "charges") }()

	params := &stripelib.ChargeListParams{}
	params.Filters.AddFilter("limit", "", "100")
	maxAt, _ := s.queries.MaxChargeCreatedAt(ctx)
	if maxAt > 0 {
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(maxAt-10, 10))
	} else {
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(s.syncFromTimestamp(ctx), 10))
	}

	iter := s.api.Charges.List(params)
	count := 0
	for iter.Next() {
		c := iter.Charge()
		charge := chargeFromStripe(c)
		if err := s.queries.UpsertStripeCharge(ctx, charge); err != nil {
			log.Printf("upsert charge %s: %v", c.ID, err)
			continue
		}
		_ = s.queries.InsertSyncLog(ctx, "charges", c.ID, "synced_from_stripe", "")
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "charges")
		return fmt.Errorf("list charges: %w", err)
	}
	log.Printf("synced %d charges", count)
	return nil
}

// SyncPayouts fetches payouts and triggers balance-transaction sync for paid ones.
// Uses MAX(created_at) from the local table as the incremental cursor.
func (s *Syncer) SyncPayouts(ctx context.Context) error {
	if err := s.queries.SetSyncStatus(ctx, "running", "payouts"); err != nil {
		return err
	}
	defer func() { _ = s.queries.SetSyncStatus(ctx, "idle", "payouts") }()

	params := &stripelib.PayoutListParams{}
	params.Filters.AddFilter("limit", "", "100")
	maxAt, _ := s.queries.MaxPayoutCreatedAt(ctx)
	if maxAt > 0 {
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(maxAt-10, 10))
	} else {
		params.Filters.AddFilter("created[gte]", "", strconv.FormatInt(s.syncFromTimestamp(ctx), 10))
	}

	iter := s.api.Payouts.List(params)
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
		count++
	}
	if err := iter.Err(); err != nil {
		_ = s.queries.SetSyncStatus(ctx, "error", "payouts")
		return fmt.Errorf("list payouts: %w", err)
	}
	log.Printf("synced %d payouts", count)
	return nil
}

// FetchAndUpsertCustomer fetches a single customer from Stripe by ID and upserts it locally.
// Used as a fallback when a customer referenced by a charge is not in the local DB.
func (s *Syncer) FetchAndUpsertCustomer(ctx context.Context, customerID string) (*db.StripeCustomer, error) {
	c, err := s.api.Customers.Get(customerID, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch customer %s from stripe: %w", customerID, err)
	}
	country := ""
	if c.Address != nil {
		country = c.Address.Country
	}
	if err := s.queries.UpsertStripeCustomer(ctx, c.ID, c.Email, c.Name, country, c.Created); err != nil {
		return nil, fmt.Errorf("upsert customer %s: %w", customerID, err)
	}
	return s.queries.GetStripeCustomer(ctx, customerID)
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
