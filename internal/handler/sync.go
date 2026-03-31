package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
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

	unsyncedChargeList, _ := h.queries.ListUnsyncedCharges(ctx)
	unsyncedPayoutList, _ := h.queries.ListUnsyncedPayouts(ctx)
	pendingVouchers, _ := h.queries.ListPendingFortnoxVouchers(ctx)

	flash := r.URL.Query().Get("flash")
	data := views.SyncPageData{
		SyncStates:         states,
		UnsyncedCharges:    int64(len(unsyncedChargeList)),
		UnsyncedPayouts:    int64(len(unsyncedPayoutList)),
		UnsyncedChargeList: unsyncedChargeList,
		UnsyncedPayoutList: unsyncedPayoutList,
		PendingVouchers:    pendingVouchers,
		Flash:              flash,
	}
	if err := views.SyncPage(data).Render(ctx, w); err != nil {
		log.Printf("render sync page: %v", err)
	}
}

// RetryPendingVoucher deletes the pending row and immediately retries sending
// the voucher to Fortnox for the given source charge or payout.
func (h *SyncHandler) RetryPendingVoucher(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Look up the pending voucher to get source type and ID.
	vouchers, err := h.queries.ListPendingFortnoxVouchers(ctx)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	var target *db.FortnoxVoucher
	for i := range vouchers {
		if vouchers[i].ID == id {
			target = &vouchers[i]
			break
		}
	}
	if target == nil {
		http.Redirect(w, r, "/sync", http.StatusSeeOther)
		return
	}

	// Delete the pending row so the source is eligible for a fresh attempt.
	if err := h.queries.DeleteFortnoxVoucher(ctx, id); err != nil {
		log.Printf("delete pending voucher %d: %v", id, err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	// Retry in background.
	go func() {
		ctx := context.Background()
		switch target.SourceType {
		case "charge":
			charges, err := h.queries.ListUnsyncedCharges(ctx)
			if err != nil {
				log.Printf("retry voucher %d: list charges: %v", id, err)
				return
			}
			for _, c := range charges {
				if c.ID == target.SourceID {
					country := c.BillingCountry.String
					if _, err := h.voucherCreator.CreateChargeVoucher(ctx, c, country); err != nil {
						log.Printf("retry charge voucher %s: %v", c.ID, err)
					}
					return
				}
			}
		case "payout":
			payouts, err := h.queries.ListUnsyncedPayouts(ctx)
			if err != nil {
				log.Printf("retry voucher %d: list payouts: %v", id, err)
				return
			}
			for _, p := range payouts {
				if p.ID == target.SourceID {
					if _, err := h.voucherCreator.CreatePayoutVoucher(ctx, p); err != nil {
						log.Printf("retry payout voucher %s: %v", p.ID, err)
					}
					return
				}
			}
		}
	}()

	http.Redirect(w, r, "/sync?flash=retry_started", http.StatusSeeOther)
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

// ListCustomers renders a paginated list of Stripe customers.
func (h *SyncHandler) ListCustomers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const pageSize = 50
	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	customers, err := h.queries.ListStripeCustomers(ctx, pageSize, offset)
	if err != nil {
		log.Printf("list customers: %v", err)
	}
	total, _ := h.queries.CountStripeCustomers(ctx)
	data := views.CustomersData{Customers: customers, Total: total, Page: page, PageSize: pageSize}
	if err := views.Customers(data).Render(ctx, w); err != nil {
		log.Printf("render customers: %v", err)
	}
}

// ListCharges renders a paginated list of Stripe charges.
func (h *SyncHandler) ListCharges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const pageSize = 50
	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	charges, err := h.queries.ListStripeCharges(ctx, pageSize, offset)
	if err != nil {
		log.Printf("list charges: %v", err)
	}
	total, _ := h.queries.CountStripeCharges(ctx)
	data := views.ChargesData{Charges: charges, Total: total, Page: page, PageSize: pageSize}
	if err := views.Charges(data).Render(ctx, w); err != nil {
		log.Printf("render charges: %v", err)
	}
}

// ListPayouts renders a paginated list of Stripe payouts.
func (h *SyncHandler) ListPayouts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const pageSize = 50
	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	payouts, err := h.queries.ListStripePayouts(ctx, pageSize, offset)
	if err != nil {
		log.Printf("list payouts: %v", err)
	}
	total, _ := h.queries.CountStripePayouts(ctx)
	data := views.PayoutsData{Payouts: payouts, Total: total, Page: page, PageSize: pageSize}
	if err := views.Payouts(data).Render(ctx, w); err != nil {
		log.Printf("render payouts: %v", err)
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
