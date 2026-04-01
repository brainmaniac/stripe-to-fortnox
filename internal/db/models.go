package db

import "database/sql"

type StripeCustomer struct {
	ID                string
	Email             sql.NullString
	Name              sql.NullString
	Country           sql.NullString
	CreatedAt         int64
	FortnoxCustomerID sql.NullString
}

type StripePaymentIntent struct {
	ID               string
	StripeCustomerID sql.NullString
	Amount           int64
	Currency         string
	Status           string
	Description      sql.NullString
	Metadata         sql.NullString
	CreatedAt        int64
	SyncedAt         sql.NullTime
	FortnoxVoucherID sql.NullInt64
}

type StripeCharge struct {
	ID                   string
	PaymentIntentID      sql.NullString
	Amount               int64
	AmountCaptured       int64
	Currency             string
	Status               string
	BalanceTransactionID sql.NullString
	CustomerID           sql.NullString
	Description          sql.NullString
	Metadata             sql.NullString
	CreatedAt            int64
	BillingCountry       sql.NullString // ISO 3166-1 alpha-2, from BillingDetails.Address.Country
	FortnoxInvoiceNumber sql.NullString // set after invoice created in Fortnox; "LEGACY" for old-flow charges
	FortnoxInvoicePaid   bool           // true after MarkInvoicePaid succeeded for this charge
}

// ChargePaymentNeeded is returned by ListChargesNeedingInvoicePayment.
// It pairs a charge (with a Fortnox invoice) with the arrival date of its already-synced payout,
// so MarkInvoicePaid can be called retroactively.
type ChargePaymentNeeded struct {
	StripeCharge
	PayoutArrivalDate int64
}

type AccountMapping struct {
	ID       int64
	Kontotyp string
	Matchtyp string
	Matchkod string
	Konto    string
	Momssats sql.NullFloat64
}

type StripePayout struct {
	ID               string
	Amount           int64
	Currency         string
	ArrivalDate      int64
	Status           string
	Description      sql.NullString
	CreatedAt        int64
	SyncedAt         sql.NullTime
	FortnoxVoucherID sql.NullInt64
}

type StripeBalanceTransaction struct {
	ID          string
	Amount      int64
	Fee         int64
	Net         int64
	Currency    string
	Type        string
	SourceID    sql.NullString
	PayoutID    sql.NullString
	CreatedAt   int64
	AvailableOn int64
	Description sql.NullString
}

type SyncState struct {
	ID           int64
	EntityType   string
	LastSyncedID sql.NullString
	LastSyncedAt sql.NullInt64
	Status       string
}

type SyncLog struct {
	ID         int64
	EntityType string
	EntityID   string
	Action     string
	Details    sql.NullString
	CreatedAt  string
}

type FortnoxVoucher struct {
	ID                   int64
	FortnoxVoucherNumber sql.NullString
	FortnoxVoucherSeries string
	VoucherDate          string
	Description          sql.NullString
	SourceType           string
	SourceID             string
	TotalDebit           int64
	TotalCredit          int64
	ResponseData         sql.NullString
	CreatedAt            string
}

type Setting struct {
	Key       string
	Value     string
	Encrypted int64
	UpdatedAt string
}
