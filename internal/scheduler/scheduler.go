package scheduler

import (
	"context"
	"log"
	"strconv"
	"time"

	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/fortnox"
	stripesync "stripe-fortnox-sync/internal/stripe"
)

const defaultIntervalMinutes = 60

// Scheduler runs a full Stripe→Fortnox sync on a configurable interval.
type Scheduler struct {
	queries        *db.Queries
	stripeSyncer   *stripesync.Syncer
	voucherCreator *fortnox.VoucherCreator
	invoiceService *fortnox.InvoiceService
}

func New(
	queries *db.Queries,
	stripeSyncer *stripesync.Syncer,
	voucherCreator *fortnox.VoucherCreator,
	invoiceService *fortnox.InvoiceService,
) *Scheduler {
	return &Scheduler{
		queries:        queries,
		stripeSyncer:   stripeSyncer,
		voucherCreator: voucherCreator,
		invoiceService: invoiceService,
	}
}

// Start launches the scheduler in the background.
func (s *Scheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

func (s *Scheduler) run(ctx context.Context) {
	for {
		interval := s.interval()
		log.Printf("scheduler: next sync in %v", interval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		s.syncAll(ctx)
	}
}

func (s *Scheduler) interval() time.Duration {
	setting, err := s.queries.GetSetting(context.Background(), "sync_interval_minutes")
	if err != nil || setting == nil || setting.Value == "0" {
		return defaultIntervalMinutes * time.Minute
	}
	minutes, err := strconv.ParseInt(setting.Value, 10, 64)
	if err != nil || minutes <= 0 {
		return defaultIntervalMinutes * time.Minute
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Scheduler) syncAll(ctx context.Context) {
	log.Printf("scheduler: starting auto-sync")

	if err := s.stripeSyncer.SyncAll(ctx); err != nil {
		log.Printf("scheduler: stripe sync error: %v", err)
	}

	// Sync charges → Fortnox invoices (B) + invoice payments (C).
	charges, err := s.queries.ListUnsyncedCharges(ctx)
	if err != nil {
		log.Printf("scheduler: list unsynced charges: %v", err)
	}
	for _, charge := range charges {
		customer, _ := s.fetchCustomer(ctx, charge.CustomerID.String)
		invoiceNum, err := s.invoiceService.CreateInvoice(ctx, charge, customer)
		if err != nil {
			log.Printf("scheduler: create invoice for charge %s: %v", charge.ID, err)
			s.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("scheduler: created fortnox invoice %s for charge %s", invoiceNum, charge.ID)
		s.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_invoice_created", invoiceNum)

		bt, err := s.queries.GetBalanceTransactionBySource(ctx, charge.ID)
		if err != nil || bt == nil {
			log.Printf("scheduler: no balance transaction for charge %s, skipping invoice payment", charge.ID)
			continue
		}
		paymentDate := time.Unix(bt.AvailableOn, 0)
		if err := s.invoiceService.MarkInvoicePaid(ctx, invoiceNum, charge.ID, charge.Amount, paymentDate); err != nil {
			log.Printf("scheduler: mark invoice paid for charge %s: %v", charge.ID, err)
		}
	}

	// Create C vouchers for charges invoiced in a previous run (balance transactions now available).
	unpaid, err := s.queries.ListChargesNeedingInvoicePayment(ctx)
	if err != nil {
		log.Printf("scheduler: list charges needing invoice payment: %v", err)
	}
	for _, r := range unpaid {
		paymentDate := time.Unix(r.AvailableOn, 0)
		if err := s.invoiceService.MarkInvoicePaid(ctx, r.FortnoxInvoiceNumber.String, r.ID, r.Amount, paymentDate); err != nil {
			log.Printf("scheduler: mark invoice paid %s for charge %s: %v", r.FortnoxInvoiceNumber.String, r.ID, err)
		}
	}

	// Sync payouts → invoicepayments + fee vouchers + payout vouchers.
	payouts, err := s.queries.ListUnsyncedPayouts(ctx)
	if err != nil {
		log.Printf("scheduler: list unsynced payouts: %v", err)
	}
	for _, payout := range payouts {
		txns, err := s.queries.ListBalanceTransactionsForPayout(ctx, payout.ID)
		if err != nil {
			log.Printf("scheduler: list balance txns for payout %s: %v", payout.ID, err)
			s.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_error", err.Error())
			continue
		}

		for _, txn := range txns {
			if txn.Type != "charge" || !txn.SourceID.Valid {
				continue
			}
			chargeID := txn.SourceID.String

			if txn.Fee > 0 {
				if _, err := s.voucherCreator.CreateFeeVoucher(ctx, chargeID, txn.Fee, payout); err != nil {
					log.Printf("scheduler: create fee voucher for charge %s: %v", chargeID, err)
				}
			}
		}

		v, err := s.voucherCreator.CreatePayoutVoucher(ctx, payout)
		if err != nil {
			log.Printf("scheduler: create payout voucher %s: %v", payout.ID, err)
			s.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("scheduler: created payout voucher %d for payout %s", v.ID, payout.ID)
		s.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_synced", "")
	}

	log.Printf("scheduler: auto-sync complete")
}

func (s *Scheduler) fetchCustomer(ctx context.Context, customerID string) (*db.StripeCustomer, error) {
	if customerID == "" {
		return &db.StripeCustomer{ID: ""}, nil
	}
	c, err := s.queries.GetStripeCustomer(ctx, customerID)
	if err != nil {
		return &db.StripeCustomer{ID: customerID}, err
	}
	if c == nil {
		log.Printf("scheduler: customer %s not in local DB, fetching from Stripe", customerID)
		fetched, err := s.stripeSyncer.FetchAndUpsertCustomer(ctx, customerID)
		if err != nil {
			log.Printf("scheduler: fetch customer %s from stripe: %v", customerID, err)
			return &db.StripeCustomer{ID: customerID}, nil
		}
		return fetched, nil
	}
	return c, nil
}
