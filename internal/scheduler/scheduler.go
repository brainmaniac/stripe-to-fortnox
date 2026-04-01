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
}

func New(queries *db.Queries, stripeSyncer *stripesync.Syncer, voucherCreator *fortnox.VoucherCreator) *Scheduler {
	return &Scheduler{
		queries:        queries,
		stripeSyncer:   stripeSyncer,
		voucherCreator: voucherCreator,
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

	charges, err := s.queries.ListUnsyncedCharges(ctx)
	if err != nil {
		log.Printf("scheduler: list unsynced charges: %v", err)
		return
	}
	for _, charge := range charges {
		country := ""
		if charge.BillingCountry.Valid {
			country = charge.BillingCountry.String
		}
		v, err := s.voucherCreator.CreateChargeVoucher(ctx, charge, country)
		if err != nil {
			log.Printf("scheduler: create charge voucher %s: %v", charge.ID, err)
			s.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("scheduler: created voucher %d for charge %s", v.ID, charge.ID)
		s.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_synced", "")
	}

	payouts, err := s.queries.ListUnsyncedPayouts(ctx)
	if err != nil {
		log.Printf("scheduler: list unsynced payouts: %v", err)
		return
	}
	for _, payout := range payouts {
		v, err := s.voucherCreator.CreatePayoutVoucher(ctx, payout)
		if err != nil {
			log.Printf("scheduler: create payout voucher %s: %v", payout.ID, err)
			s.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("scheduler: created voucher %d for payout %s", v.ID, payout.ID)
		s.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_synced", "")
	}

	log.Printf("scheduler: auto-sync complete")
}
