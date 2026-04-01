package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

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
	invoiceService *fortnox.InvoiceService
}

func NewSyncHandler(
	queries *db.Queries,
	stripeSyncer *stripesync.Syncer,
	voucherCreator *fortnox.VoucherCreator,
	invoiceService *fortnox.InvoiceService,
) *SyncHandler {
	return &SyncHandler{
		queries:        queries,
		stripeSyncer:   stripeSyncer,
		voucherCreator: voucherCreator,
		invoiceService: invoiceService,
	}
}

// TriggerStripeSync starts a full Stripe data pull in the background.
func (h *SyncHandler) TriggerStripeSync(w http.ResponseWriter, r *http.Request) {
	go func() {
		if err := h.stripeSyncer.SyncAll(context.Background()); err != nil {
			log.Printf("stripe sync error: %v", err)
		}
	}()
	if r.Header.Get("HX-Request") == "true" {
		h.renderSyncStatus(w, r, "sync_started")
		return
	}
	http.Redirect(w, r, "/sync?flash=sync_started", http.StatusSeeOther)
}

// TriggerFortnoxSync processes all unsynced charges and payouts into Fortnox.
// Charges → Fortnox invoices (B-series); payouts → invoicepayments + fee vouchers + payout voucher (C-series).
func (h *SyncHandler) TriggerFortnoxSync(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx := context.Background()
		syncChargesToFortnox(ctx, h.queries, h.stripeSyncer, h.invoiceService)
		syncPayoutsToFortnox(ctx, h.queries, h.invoiceService, h.voucherCreator)
	}()
	if r.Header.Get("HX-Request") == "true" {
		h.renderSyncStatus(w, r, "fortnox_sync_started")
		return
	}
	http.Redirect(w, r, "/sync?flash=fortnox_sync_started", http.StatusSeeOther)
}

// SyncStatus returns just the live status section (used by HTMX polling).
func (h *SyncHandler) SyncStatus(w http.ResponseWriter, r *http.Request) {
	h.renderSyncStatus(w, r, "")
}

func (h *SyncHandler) renderSyncStatus(w http.ResponseWriter, r *http.Request, flash string) {
	ctx := r.Context()
	states, _ := h.queries.ListSyncStates(ctx)
	unsyncedChargeList, _ := h.queries.ListUnsyncedCharges(ctx)
	unsyncedPayoutList, _ := h.queries.ListUnsyncedPayouts(ctx)
	pendingVouchers, _ := h.queries.ListPendingFortnoxVouchers(ctx)
	data := views.SyncPageData{
		SyncStates:         states,
		UnsyncedCharges:    int64(len(unsyncedChargeList)),
		UnsyncedPayouts:    int64(len(unsyncedPayoutList)),
		UnsyncedChargeList: unsyncedChargeList,
		UnsyncedPayoutList: unsyncedPayoutList,
		PendingVouchers:    pendingVouchers,
		Flash:              flash,
	}
	if err := views.SyncStatusSection(data).Render(ctx, w); err != nil {
		log.Printf("render sync status: %v", err)
	}
}

// syncChargesToFortnox creates a Fortnox invoice for each unsynced Stripe charge.
func syncChargesToFortnox(ctx context.Context, queries *db.Queries, stripeSyncer *stripesync.Syncer, invoiceService *fortnox.InvoiceService) {
	charges, err := queries.ListUnsyncedCharges(ctx)
	if err != nil {
		log.Printf("list unsynced charges: %v", err)
		return
	}
	for _, charge := range charges {
		customer, err := fetchOrPlaceholderCustomer(ctx, queries, stripeSyncer, charge.CustomerID.String)
		if err != nil {
			log.Printf("get customer for charge %s: %v", charge.ID, err)
		}
		invoiceNum, err := invoiceService.CreateInvoice(ctx, charge, customer)
		if err != nil {
			log.Printf("create invoice for charge %s: %v", charge.ID, err)
			queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("created fortnox invoice %s for charge %s", invoiceNum, charge.ID)
		queries.InsertSyncLog(ctx, "charges", charge.ID, "fortnox_invoice_created", invoiceNum)
	}
}

// syncPayoutsToFortnox processes each unsynced payout:
// for every charge in the payout's balance transactions it records the invoice payment
// and creates a fee voucher, then creates the payout voucher (bank ← clearing).
func syncPayoutsToFortnox(
	ctx context.Context,
	queries *db.Queries,
	invoiceService *fortnox.InvoiceService,
	voucherCreator *fortnox.VoucherCreator,
) {
	payouts, err := queries.ListUnsyncedPayouts(ctx)
	if err != nil {
		log.Printf("list unsynced payouts: %v", err)
		return
	}
	for _, payout := range payouts {
		if err := processPayout(ctx, queries, invoiceService, voucherCreator, payout); err != nil {
			log.Printf("process payout %s: %v", payout.ID, err)
			queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_error", err.Error())
			continue
		}
		log.Printf("synced payout %s to fortnox", payout.ID)
		queries.InsertSyncLog(ctx, "payouts", payout.ID, "fortnox_synced", "")
	}
}

func processPayout(
	ctx context.Context,
	queries *db.Queries,
	invoiceService *fortnox.InvoiceService,
	voucherCreator *fortnox.VoucherCreator,
	payout db.StripePayout,
) error {
	payoutDate := time.Unix(payout.ArrivalDate, 0)

	txns, err := queries.ListBalanceTransactionsForPayout(ctx, payout.ID)
	if err != nil {
		return err
	}

	for _, txn := range txns {
		if txn.Type != "charge" || !txn.SourceID.Valid {
			continue
		}
		chargeID := txn.SourceID.String

		charge, err := queries.GetStripeCharge(ctx, chargeID)
		if err != nil || charge == nil {
			log.Printf("get charge %s for payout %s: %v", chargeID, payout.ID, err)
			continue
		}

		// Mark the invoice paid if the charge was synced via the invoice flow.
		if charge.FortnoxInvoiceNumber.Valid &&
			charge.FortnoxInvoiceNumber.String != "" &&
			charge.FortnoxInvoiceNumber.String != "LEGACY" {
			if err := invoiceService.MarkInvoicePaid(ctx, charge.FortnoxInvoiceNumber.String, charge.Amount, payoutDate); err != nil {
				log.Printf("mark invoice paid %s for charge %s: %v", charge.FortnoxInvoiceNumber.String, chargeID, err)
			}
		}

		// Create fee voucher for the Stripe processing fee (omvänd moms applies).
		if txn.Fee > 0 {
			txnDate := time.Unix(txn.CreatedAt, 0)
			if _, err := voucherCreator.CreateFeeVoucher(ctx, chargeID, txn.Fee, txnDate); err != nil {
				log.Printf("create fee voucher for charge %s: %v", chargeID, err)
			}
		}
	}

	// Create payout voucher: debit bank account, credit Stripe clearing.
	// This also marks the payout as synced (used by ListUnsyncedPayouts).
	if _, err := voucherCreator.CreatePayoutVoucher(ctx, payout); err != nil {
		return err
	}

	return nil
}

// fetchOrPlaceholderCustomer retrieves a customer from the local DB.
// If not found, it fetches the customer directly from Stripe and upserts it locally.
// Falls back to a placeholder if the Stripe fetch also fails.
func fetchOrPlaceholderCustomer(ctx context.Context, queries *db.Queries, stripeSyncer *stripesync.Syncer, customerID string) (*db.StripeCustomer, error) {
	if customerID == "" {
		return &db.StripeCustomer{ID: ""}, nil
	}
	c, err := queries.GetStripeCustomer(ctx, customerID)
	if err != nil {
		return &db.StripeCustomer{ID: customerID}, err
	}
	if c == nil {
		log.Printf("customer %s not in local DB, fetching from Stripe", customerID)
		fetched, err := stripeSyncer.FetchAndUpsertCustomer(ctx, customerID)
		if err != nil {
			log.Printf("fetch customer %s from stripe: %v", customerID, err)
			return &db.StripeCustomer{ID: customerID}, nil
		}
		return fetched, nil
	}
	return c, nil
}

// SyncPage renders the full sync page (initial load).
func (h *SyncHandler) SyncPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	states, _ := h.queries.ListSyncStates(ctx)
	unsyncedChargeList, _ := h.queries.ListUnsyncedCharges(ctx)
	unsyncedPayoutList, _ := h.queries.ListUnsyncedPayouts(ctx)
	pendingVouchers, _ := h.queries.ListPendingFortnoxVouchers(ctx)
	data := views.SyncPageData{
		SyncStates:         states,
		UnsyncedCharges:    int64(len(unsyncedChargeList)),
		UnsyncedPayouts:    int64(len(unsyncedPayoutList)),
		UnsyncedChargeList: unsyncedChargeList,
		UnsyncedPayoutList: unsyncedPayoutList,
		PendingVouchers:    pendingVouchers,
		Flash:              r.URL.Query().Get("flash"),
	}
	if err := views.SyncPage(data).Render(ctx, w); err != nil {
		log.Printf("render sync page: %v", err)
	}
}

// RetryPendingVoucher deletes the pending row and immediately retries sending
// the voucher to Fortnox for the given source payout or fee.
func (h *SyncHandler) RetryPendingVoucher(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

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

	if err := h.queries.DeleteFortnoxVoucher(ctx, id); err != nil {
		log.Printf("delete pending voucher %d: %v", id, err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	go func() {
		ctx := context.Background()
		switch target.SourceType {
		case "payout":
			payouts, err := h.queries.ListUnsyncedPayouts(ctx)
			if err != nil {
				log.Printf("retry voucher %d: list payouts: %v", id, err)
				return
			}
			for _, p := range payouts {
				if p.ID == target.SourceID {
					if err := processPayout(ctx, h.queries, h.invoiceService, h.voucherCreator, p); err != nil {
						log.Printf("retry payout %s: %v", p.ID, err)
					}
					return
				}
			}
		case "fee":
			// Fee vouchers are retried by re-running the payout sync.
			log.Printf("retry voucher %d: fee retry not supported standalone; re-run payout sync", id)
		}
	}()

	if r.Header.Get("HX-Request") == "true" {
		h.renderSyncStatus(w, r, "retry_started")
		return
	}
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
