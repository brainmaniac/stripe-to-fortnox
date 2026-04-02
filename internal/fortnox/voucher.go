package fortnox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// EU member state country codes.
var euCountries = map[string]bool{
	"AT": true, "BE": true, "BG": true, "CY": true, "CZ": true,
	"DE": true, "DK": true, "EE": true, "ES": true, "FI": true,
	"FR": true, "GR": true, "HR": true, "HU": true, "IE": true,
	"IT": true, "LT": true, "LU": true, "LV": true, "MT": true,
	"NL": true, "PL": true, "PT": true, "RO": true, "SI": true,
	"SK": true,
}

// AccountConfig holds the configurable account numbers for voucher creation.
type AccountConfig struct {
	StripeClearing   string
	BankAccount      string
	RevenueSE        string
	RevenueEU        string
	RevenueWO        string
	OutputVAT25      string
	PaymentFee       string
	ReverseVATDebit  string
	ReverseVATCredit string
	VoucherSeries    string
	VATPercent       float64
}

// DefaultAccountConfig returns the configured defaults.
func DefaultAccountConfig() AccountConfig {
	return AccountConfig{
		StripeClearing:   AccountStripeClearing,
		BankAccount:      AccountBankAccount,
		RevenueSE:        AccountRevenueSE,
		RevenueEU:        AccountRevenueEU,
		RevenueWO:        AccountRevenueWO,
		OutputVAT25:      AccountOutputVAT25,
		PaymentFee:       AccountPaymentFee,
		ReverseVATDebit:  AccountReverseVATDebit,
		ReverseVATCredit: AccountReverseVATCredit,
		VoucherSeries:    "A",
		VATPercent:       25.0,
	}
}

// revenueAccount returns the correct intäktskonto for a billing country code.
func (cfg AccountConfig) revenueAccount(countryCode string) string {
	if countryCode == "" || countryCode == "SE" {
		return cfg.RevenueSE
	}
	if euCountries[countryCode] {
		return cfg.RevenueEU
	}
	return cfg.RevenueWO
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

// CreateChargeVoucher creates a Fortnox voucher for a succeeded Stripe charge.
// countryCode is the billing country (e.g. "SE", "DE", "US"). Empty defaults to SE.
func (vc *VoucherCreator) CreateChargeVoucher(ctx context.Context, charge db.StripeCharge, countryCode string) (*db.FortnoxVoucher, error) {
	existing, err := vc.queries.GetFortnoxVoucherBySource(ctx, "charge", charge.ID)
	if err != nil {
		return nil, err
	}
	// Only skip if the voucher is confirmed in Fortnox (has a voucher number).
	// A pending row (NULL voucher number) means a previous attempt started but
	// didn't finish — we should retry the Fortnox call.
	if existing != nil && existing.FortnoxVoucherNumber.Valid {
		return existing, nil
	}

	amount := toMajorUnit(charge.Amount)
	revenueAcc := vc.config.revenueAccount(countryCode)

	var rows []VoucherRow
	rows = append(rows, VoucherRow{Account: vc.config.StripeClearing, Debit: amount})

	if countryCode == "" || countryCode == "SE" {
		// Swedish domestic sale — split revenue and output VAT.
		vatRate := vc.config.VATPercent / 100.0
		vatAmount := amount * vatRate / (1 + vatRate)
		revenue := amount - vatAmount
		rows = append(rows,
			VoucherRow{Account: revenueAcc, Credit: revenue},
			VoucherRow{Account: vc.config.OutputVAT25, Credit: vatAmount},
		)
	} else {
		// EU / export — full amount to revenue, no Swedish VAT.
		rows = append(rows, VoucherRow{Account: revenueAcc, Credit: amount})
	}

	req := VoucherRequest{}
	req.Voucher.Description = fmt.Sprintf("Stripe charge %s", charge.ID)
	req.Voucher.VoucherSeries = vc.config.VoucherSeries
	req.Voucher.TransactionDate = time.Unix(charge.CreatedAt, 0).Format("2006-01-02")
	req.Voucher.VoucherRows = rows

	return vc.postVoucher(ctx, req, "charge", charge.ID)
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
	req.Voucher.Description = fmt.Sprintf("Stripe utbetalning %s", payout.ID)
	req.Voucher.VoucherSeries = vc.config.VoucherSeries
	req.Voucher.TransactionDate = time.Unix(payout.ArrivalDate, 0).Format("2006-01-02")
	req.Voucher.VoucherRows = []VoucherRow{
		{Account: vc.config.BankAccount, Debit: amount},
		{Account: vc.config.StripeClearing, Credit: amount},
	}

	return vc.postVoucher(ctx, req, "payout", payout.ID)
}

// CreateFeeVoucher creates a Fortnox voucher for a Stripe processing fee with reverse VAT.
// Stripe Ltd (Ireland) is an EU service provider → omvänd skattskyldighet applies.
func (vc *VoucherCreator) CreateFeeVoucher(ctx context.Context, chargeID string, feeOre int64, date time.Time) (*db.FortnoxVoucher, error) {
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

	req := VoucherRequest{}
	req.Voucher.Description = fmt.Sprintf("Stripe avgift för charge %s", chargeID)
	req.Voucher.VoucherSeries = vc.config.VoucherSeries
	req.Voucher.TransactionDate = date.Format("2006-01-02")
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
