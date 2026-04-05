package fortnox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"stripe-fortnox-sync/internal/db"
)

// BAS-kontoplan account numbers.
const (
	AccountStripeClearing   = "1521" // Avstämningskonto Stripe
	AccountBankAccount      = "1930" // Utbetalningskonto bank
	AccountRevenueSE        = "3010" // Intäktskonto Sverige
	AccountRevenueEU        = "3007" // Intäktskonto EU
	AccountRevenueWO        = "3008" // Intäktskonto utanför EU
	AccountOutputVAT25      = "2611" // Utgående moms 25%
	AccountPaymentFee       = "6065" // Konto betalväxelavgift
	AccountReverseVATDebit  = "2645" // Omvänd moms betalväxelavgift – debet
	AccountReverseVATCredit = "2614" // Omvänd moms betalväxelavgift – kredit

	// Legacy aliases kept for settings migration.
	AccountStripeReceivable = AccountStripeClearing
	AccountBankFees         = AccountPaymentFee
	AccountRevenue25VAT     = AccountRevenueSE
)

// AccountConfig holds the configurable account numbers for fee and payout voucher creation.
type AccountConfig struct {
	StripeClearing   string
	BankAccount      string
	PaymentFee       string
	ReverseVATDebit  string
	ReverseVATCredit string
	VoucherSeries    string
	VATPercent       float64 // VAT rate for reverse-VAT fee vouchers (omvänd moms), e.g. 25.0
}

// DefaultAccountConfig returns the configured defaults.
func DefaultAccountConfig() AccountConfig {
	return AccountConfig{
		StripeClearing:   AccountStripeClearing,
		BankAccount:      AccountBankAccount,
		PaymentFee:       AccountPaymentFee,
		ReverseVATDebit:  AccountReverseVATDebit,
		ReverseVATCredit: AccountReverseVATCredit,
		VoucherSeries:    "S",
		VATPercent:       25.0,
	}
}

// VoucherRow represents a single accounting row in a Fortnox voucher.
type VoucherRow struct {
	Account string  `json:"Account"`
	Debit   float64 `json:"Debit"`
	Credit  float64 `json:"Credit"`
}

// VoucherRequest is the JSON payload sent to Fortnox.
type VoucherRequest struct {
	Voucher struct {
		Description     string       `json:"Description"`
		VoucherSeries   string       `json:"VoucherSeries"`
		TransactionDate string       `json:"TransactionDate"`
		VoucherRows     []VoucherRow `json:"VoucherRows"`
	} `json:"Voucher"`
}

// VoucherResponse is the JSON response from Fortnox.
type VoucherResponse struct {
	Voucher struct {
		VoucherNumber string `json:"VoucherNumber"`
		VoucherSeries string `json:"VoucherSeries"`
	} `json:"Voucher"`
}

// Poster is the subset of APIClient used by VoucherCreator and InvoiceService (enables testing without a real HTTP client).
type Poster interface {
	Post(ctx context.Context, path string, body interface{}) ([]byte, error)
	Put(ctx context.Context, path string, body interface{}) ([]byte, error)
}

// VoucherCreator creates Fortnox vouchers from Stripe events.
type VoucherCreator struct {
	api     Poster
	queries *db.Queries
	config  AccountConfig
}

func NewVoucherCreator(api *APIClient, queries *db.Queries, config AccountConfig) *VoucherCreator {
	return &VoucherCreator{api: api, queries: queries, config: config}
}

// toMajorUnit converts a minor currency unit (cents, öre, pence) to the major unit (dollar, krona, pound).
func toMajorUnit(minor int64) float64 {
	return float64(minor) / 100.0
}

// CreatePayoutVoucher creates a Fortnox voucher for a Stripe payout.
func (vc *VoucherCreator) CreatePayoutVoucher(ctx context.Context, payout db.StripePayout) (*db.FortnoxVoucher, error) {
	existing, err := vc.queries.GetFortnoxVoucherBySource(ctx, "payout", payout.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.FortnoxVoucherNumber.Valid {
		return existing, nil
	}

	amount := toMajorUnit(payout.Amount)
	req := VoucherRequest{}
	date := time.Unix(payout.ArrivalDate, 0).Format("2006-01-02")
	req.Voucher.Description = fmt.Sprintf("Stripe Utbetalning - %s - %s - ID %s", date, strings.ToUpper(payout.Currency), payout.ID)
	req.Voucher.VoucherSeries = vc.config.VoucherSeries
	req.Voucher.TransactionDate = date
	req.Voucher.VoucherRows = []VoucherRow{
		{Account: vc.config.BankAccount, Debit: amount},
		{Account: vc.config.StripeClearing, Credit: amount},
	}

	return vc.postVoucher(ctx, req, "payout", payout.ID)
}

// CreateFeeVoucher creates a Fortnox voucher for a Stripe processing fee with reverse VAT.
// Stripe Ltd (Ireland) is an EU service provider → omvänd skattskyldighet applies.
// The description matches the payout voucher so both S-series entries reference the same payout.
func (vc *VoucherCreator) CreateFeeVoucher(ctx context.Context, chargeID string, feeOre int64, payout db.StripePayout) (*db.FortnoxVoucher, error) {
	sourceID := "fee_" + chargeID
	existing, err := vc.queries.GetFortnoxVoucherBySource(ctx, "fee", sourceID)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.FortnoxVoucherNumber.Valid {
		return existing, nil
	}

	fee := toMajorUnit(feeOre)
	reverseVAT := fee * (vc.config.VATPercent / 100.0)

	date := time.Unix(payout.ArrivalDate, 0).Format("2006-01-02")
	req := VoucherRequest{}
	req.Voucher.Description = fmt.Sprintf("Stripe Utbetalning - %s - %s - ID %s - Stripe Billing Fee", date, strings.ToUpper(payout.Currency), payout.ID)
	req.Voucher.VoucherSeries = vc.config.VoucherSeries
	req.Voucher.TransactionDate = date
	req.Voucher.VoucherRows = []VoucherRow{
		{Account: vc.config.PaymentFee, Debit: fee},
		{Account: vc.config.ReverseVATDebit, Debit: reverseVAT},
		{Account: vc.config.ReverseVATCredit, Credit: reverseVAT},
		{Account: vc.config.StripeClearing, Credit: fee},
	}

	return vc.postVoucher(ctx, req, "fee", sourceID)
}

func (vc *VoucherCreator) postVoucher(
	ctx context.Context,
	req VoucherRequest,
	sourceType, sourceID string,
) (*db.FortnoxVoucher, error) {
	// Sanity check: debit must equal credit.
	var totalDebit, totalCredit float64
	for _, row := range req.Voucher.VoucherRows {
		totalDebit += row.Debit
		totalCredit += row.Credit
	}
	if fmt.Sprintf("%.2f", totalDebit) != fmt.Sprintf("%.2f", totalCredit) {
		return nil, fmt.Errorf("verifikat obalanserat: debet=%.2f kredit=%.2f", totalDebit, totalCredit)
	}

	totalOre := int64(totalDebit * 100)
	pending := db.FortnoxVoucher{
		FortnoxVoucherSeries: vc.config.VoucherSeries,
		VoucherDate:          req.Voucher.TransactionDate,
		Description:          sql.NullString{String: req.Voucher.Description, Valid: req.Voucher.Description != ""},
		SourceType:           sourceType,
		SourceID:             sourceID,
		TotalDebit:           totalOre,
		TotalCredit:          totalOre,
	}

	// Phase 1: reserve a local row BEFORE calling Fortnox.
	// INSERT OR IGNORE means a concurrent runner is silently skipped.
	if err := vc.queries.InsertPendingFortnoxVoucher(ctx, pending); err != nil {
		return nil, fmt.Errorf("reservera lokal rad: %w", err)
	}

	// Phase 2: call Fortnox. If this crashes after a successful response but
	// before the UPDATE below, the pending row remains and the next run will
	// retry only the Fortnox call (not create a second pending row).
	respBody, err := vc.api.Post(ctx, "vouchers", req)
	if err != nil {
		return nil, fmt.Errorf("post verifikat till fortnox: %w", err)
	}

	// Phase 3: update the local row with the confirmed voucher number.
	var voucherResp VoucherResponse
	_ = json.Unmarshal(respBody, &voucherResp)

	if err := vc.queries.ConfirmFortnoxVoucher(ctx,
		voucherResp.Voucher.VoucherNumber,
		string(respBody),
		sourceType, sourceID,
	); err != nil {
		return nil, fmt.Errorf("bekräfta verifikat i db: %w", err)
	}

	return vc.queries.GetFortnoxVoucherBySource(ctx, sourceType, sourceID)
}
