package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"stripe-fortnox-sync/internal/db"
	"stripe-fortnox-sync/internal/fortnox"
	stripesync "stripe-fortnox-sync/internal/stripe"
	"stripe-fortnox-sync/internal/views"
)

// SyncHandler manages triggering Stripe→Fortnox sync operations.
type SyncHandler struct {
	queries        *db.Queries
	stripeSyncer   *stripesync.Syncer
	voucherCreator *fortnox.VoucherCreator
}

func NewSyncHandler(
	queries *db.Queries,
	stripeSyncer *stripesync.Syncer,
	voucherCreator *fortnox.VoucherCreator,
) *SyncHandler {
	return &SyncHandler{
		queries:        queries,
		stripeSyncer:   stripeSyncer,
		voucherCreator: voucherCreator,
	}
}

// TriggerStripeSync starts a full Stripe data pull in the background and redirects.
func (h *SyncHandler) TriggerStripeSync(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := h.stripeSyncer.SyncAll(context.Background()); err != nil {
			log.Printf("stripe sync error: %v", err)
		}
	}()
	http.Redirect(w, r, "/sync?flash=sync_started", http.StatusSeeOther)
}

// TriggerFortnoxSync processes all unsynced charges and payouts into Fortnox vouchers.
func (h *SyncHandler) TriggerFortnoxSync(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx := context.Background()

		charges, err := h.queries.ListUnsyncedCharges(ctx)
		if err != nil {
			log.Printf("list unsynced charges: %v", err)
			return
		}
		for _, charge := range charges {
			country := ""
			if charge.BillingCountry.Valid {
				country = charge.BillingCountry.String
			}
			v, err := h.voucherCreator.CreateChargeVoucher(ctx, charge, country)
			if err != nil {
				log.Printf("create charge voucher %s: %v", charge.ID, err)
				h.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_error", err.Error())
				continue
			}
			log.Printf("created voucher %d for charge %s", v.ID, charge.ID)
			h.queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_synced", "")
		}

		payouts, err := h.queries.ListUnsyncedPayouts(ctx)
		if err != nil {
			log.Printf("list unsynced payouts: %v", err)
			return
		}
		for _, payout := range payouts {
			v, err := h.voucherCreator.CreatePayoutVoucher(ctx, payout)
			if err != nil {
				log.Printf("create payout voucher %s: %v", payout.ID, err)
				h.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_error", err.Error())
				continue
			}
			log.Printf("created voucher %d for payout %s", v.ID, payout.ID)
			h.queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_synced", "")
		}
	}()

	http.Redirect(w, r, "/sync?flash=fortnox_sync_started", http.StatusSeeOther)
}

// SyncPage renders a page with sync status and controls.
func (h *SyncHandler) SyncPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	states, err := h.queries.ListSyncStates(ctx)
	if err != nil {
		log.Printf("list sync states: %v", err)
	}

	unsyncedCharges, _ := h.queries.CountUnsyncedCharges(ctx)
	unsyncedPayouts, _ := h.queries.CountUnsyncedPayouts(ctx)

	flash := r.URL.Query().Get("flash")
	data := views.SyncPageData{
		SyncStates:      states,
		UnsyncedCharges: unsyncedCharges,
		UnsyncedPayouts: unsyncedPayouts,
		Flash:           flash,
	}
	if err := views.SyncPage(data).Render(ctx, w); err != nil {
		log.Printf("render sync page: %v", err)
	}
}

// ListVouchers renders a paginated list of Fortnox vouchers.
func (h *SyncHandler) ListVouchers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const pageSize = 50

	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize

	vouchers, err := h.queries.ListFortnoxVouchers(ctx, pageSize, offset)
	if err != nil {
		log.Printf("list vouchers: %v", err)
	}
	total, _ := h.queries.CountFortnoxVouchers(ctx)

	data := views.VouchersData{
		Vouchers: vouchers,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	if err := views.Vouchers(data).Render(ctx, w); err != nil {
		log.Printf("render vouchers: %v", err)
	}
}

// SyncLogs renders recent sync log entries.
func (h *SyncHandler) SyncLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	logs, err := h.queries.ListRecentSyncLogs(ctx, 100)
	if err != nil {
		log.Printf("list sync logs: %v", err)
	}

	if err := views.Logs(logs).Render(ctx, w); err != nil {
		log.Printf("render logs: %v", err)
	}
}
