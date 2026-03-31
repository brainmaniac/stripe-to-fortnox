package db

import (
	"context"
	"database/sql"
)

// nullStr returns sql.NullString for a string value.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// ── Stripe Customers ──────────────────────────────────────────────────────────

func (q *Queries) UpsertStripeCustomer(ctx context.Context, id, email, name, country string, createdAt int64) error {
	const query = `
INSERT INTO stripe_customers (id, email, name, country, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    email   = excluded.email,
    name    = excluded.name,
    country = excluded.country`
	_, err := q.db.ExecContext(ctx, query, id, nullStr(email), nullStr(name), nullStr(country), createdAt)
	return err
}

func (q *Queries) GetStripeCustomer(ctx context.Context, id string) (*StripeCustomer, error) {
	const query = `
SELECT id, email, name, country, created_at, fortnox_customer_id
FROM stripe_customers WHERE id = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, id)
	c := &StripeCustomer{}
	err := row.Scan(&c.ID, &c.Email, &c.Name, &c.Country, &c.CreatedAt, &c.FortnoxCustomerID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (q *Queries) SetFortnoxCustomerID(ctx context.Context, fortnoxID, stripeID string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE stripe_customers SET fortnox_customer_id = ? WHERE id = ?`, fortnoxID, stripeID)
	return err
}

func (q *Queries) MaxCustomerCreatedAt(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	err := q.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM stripe_customers`).Scan(&ts)
	if err != nil || !ts.Valid {
		return 0, err
	}
	return ts.Int64, nil
}

func (q *Queries) MaxChargeCreatedAt(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	err := q.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM stripe_charges`).Scan(&ts)
	if err != nil || !ts.Valid {
		return 0, err
	}
	return ts.Int64, nil
}

func (q *Queries) MaxPayoutCreatedAt(ctx context.Context) (int64, error) {
	var ts sql.NullInt64
	err := q.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM stripe_payouts`).Scan(&ts)
	if err != nil || !ts.Valid {
		return 0, err
	}
	return ts.Int64, nil
}

func (q *Queries) ListStripeCustomers(ctx context.Context, limit, offset int64) ([]StripeCustomer, error) {
	const query = `
SELECT id, email, name, country, created_at, fortnox_customer_id
FROM stripe_customers ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := q.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var customers []StripeCustomer
	for rows.Next() {
		c := StripeCustomer{}
		if err := rows.Scan(&c.ID, &c.Email, &c.Name, &c.Country, &c.CreatedAt, &c.FortnoxCustomerID); err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func (q *Queries) ListStripeCharges(ctx context.Context, limit, offset int64) ([]StripeCharge, error) {
	const query = `
SELECT id, payment_intent_id, amount, amount_captured, currency, status,
       balance_transaction_id, customer_id, description, metadata, created_at, billing_country
FROM stripe_charges ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := q.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var charges []StripeCharge
	for rows.Next() {
		c := StripeCharge{}
		if err := rows.Scan(&c.ID, &c.PaymentIntentID, &c.Amount, &c.AmountCaptured,
			&c.Currency, &c.Status, &c.BalanceTransactionID, &c.CustomerID,
			&c.Description, &c.Metadata, &c.CreatedAt, &c.BillingCountry); err != nil {
			return nil, err
		}
		charges = append(charges, c)
	}
	return charges, rows.Err()
}

func (q *Queries) ListStripePayouts(ctx context.Context, limit, offset int64) ([]StripePayout, error) {
	const query = `
SELECT id, amount, currency, arrival_date, status, description, created_at, synced_at, fortnox_voucher_id
FROM stripe_payouts ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := q.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []StripePayout
	for rows.Next() {
		p := StripePayout{}
		if err := rows.Scan(&p.ID, &p.Amount, &p.Currency, &p.ArrivalDate,
			&p.Status, &p.Description, &p.CreatedAt, &p.SyncedAt, &p.FortnoxVoucherID); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

func (q *Queries) CountStripeCustomers(ctx context.Context) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stripe_customers`).Scan(&count)
	return count, err
}

// ── Stripe Charges ────────────────────────────────────────────────────────────

func (q *Queries) UpsertStripeCharge(ctx context.Context, c StripeCharge) error {
	const query = `
INSERT INTO stripe_charges (id, payment_intent_id, amount, amount_captured, currency, status,
    balance_transaction_id, customer_id, description, metadata, created_at, billing_country)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    status                 = excluded.status,
    amount_captured        = excluded.amount_captured,
    balance_transaction_id = excluded.balance_transaction_id,
    billing_country        = COALESCE(excluded.billing_country, billing_country)`
	_, err := q.db.ExecContext(ctx, query,
		c.ID, c.PaymentIntentID, c.Amount, c.AmountCaptured, c.Currency,
		c.Status, c.BalanceTransactionID, c.CustomerID, c.Description, c.Metadata, c.CreatedAt,
		c.BillingCountry)
	return err
}

func (q *Queries) GetStripeCharge(ctx context.Context, id string) (*StripeCharge, error) {
	const query = `
SELECT id, payment_intent_id, amount, amount_captured, currency, status,
    balance_transaction_id, customer_id, description, metadata, created_at, billing_country
FROM stripe_charges WHERE id = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, id)
	c := &StripeCharge{}
	err := row.Scan(&c.ID, &c.PaymentIntentID, &c.Amount, &c.AmountCaptured, &c.Currency,
		&c.Status, &c.BalanceTransactionID, &c.CustomerID, &c.Description, &c.Metadata, &c.CreatedAt,
		&c.BillingCountry)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

func (q *Queries) ListUnsyncedCharges(ctx context.Context) ([]StripeCharge, error) {
	const query = `
SELECT sc.id, sc.payment_intent_id, sc.amount, sc.amount_captured, sc.currency, sc.status,
    sc.balance_transaction_id, sc.customer_id, sc.description, sc.metadata, sc.created_at, sc.billing_country
FROM stripe_charges sc
LEFT JOIN fortnox_vouchers fv ON fv.source_type = 'charge' AND fv.source_id = sc.id
WHERE sc.status = 'succeeded' AND fv.id IS NULL
ORDER BY sc.created_at ASC`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var charges []StripeCharge
	for rows.Next() {
		c := StripeCharge{}
		if err := rows.Scan(&c.ID, &c.PaymentIntentID, &c.Amount, &c.AmountCaptured, &c.Currency,
			&c.Status, &c.BalanceTransactionID, &c.CustomerID, &c.Description, &c.Metadata, &c.CreatedAt,
			&c.BillingCountry); err != nil {
			return nil, err
		}
		charges = append(charges, c)
	}
	return charges, rows.Err()
}

func (q *Queries) CountUnsyncedCharges(ctx context.Context) (int64, error) {
	const query = `
SELECT COUNT(*)
FROM stripe_charges sc
LEFT JOIN fortnox_vouchers fv ON fv.source_type = 'charge' AND fv.source_id = sc.id
WHERE sc.status = 'succeeded' AND fv.id IS NULL`
	var count int64
	err := q.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func (q *Queries) CountStripeCharges(ctx context.Context) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stripe_charges`).Scan(&count)
	return count, err
}

// ── Stripe Payouts ────────────────────────────────────────────────────────────

func (q *Queries) UpsertStripePayout(ctx context.Context, p StripePayout) error {
	const query = `
INSERT INTO stripe_payouts (id, amount, currency, arrival_date, status, description, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    status       = excluded.status,
    arrival_date = excluded.arrival_date`
	_, err := q.db.ExecContext(ctx, query,
		p.ID, p.Amount, p.Currency, p.ArrivalDate, p.Status, p.Description, p.CreatedAt)
	return err
}

func (q *Queries) ListUnsyncedPayouts(ctx context.Context) ([]StripePayout, error) {
	const query = `
SELECT sp.id, sp.amount, sp.currency, sp.arrival_date, sp.status, sp.description,
    sp.created_at, sp.synced_at, sp.fortnox_voucher_id
FROM stripe_payouts sp
LEFT JOIN fortnox_vouchers fv ON fv.source_type = 'payout' AND fv.source_id = sp.id
WHERE sp.status = 'paid' AND fv.id IS NULL
ORDER BY sp.created_at ASC`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var payouts []StripePayout
	for rows.Next() {
		p := StripePayout{}
		if err := rows.Scan(&p.ID, &p.Amount, &p.Currency, &p.ArrivalDate, &p.Status,
			&p.Description, &p.CreatedAt, &p.SyncedAt, &p.FortnoxVoucherID); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

func (q *Queries) CountUnsyncedPayouts(ctx context.Context) (int64, error) {
	const query = `
SELECT COUNT(*)
FROM stripe_payouts sp
LEFT JOIN fortnox_vouchers fv ON fv.source_type = 'payout' AND fv.source_id = sp.id
WHERE sp.status = 'paid' AND fv.id IS NULL`
	var count int64
	err := q.db.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func (q *Queries) CountStripePayouts(ctx context.Context) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stripe_payouts`).Scan(&count)
	return count, err
}

// ── Balance Transactions ──────────────────────────────────────────────────────

func (q *Queries) UpsertStripeBalanceTransaction(ctx context.Context, bt StripeBalanceTransaction) error {
	const query = `
INSERT INTO stripe_balance_transactions
    (id, amount, fee, net, currency, type, source_id, payout_id, created_at, available_on, description)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET payout_id = excluded.payout_id`
	_, err := q.db.ExecContext(ctx, query,
		bt.ID, bt.Amount, bt.Fee, bt.Net, bt.Currency, bt.Type,
		bt.SourceID, bt.PayoutID, bt.CreatedAt, bt.AvailableOn, bt.Description)
	return err
}

func (q *Queries) ListBalanceTransactionsForPayout(ctx context.Context, payoutID string) ([]StripeBalanceTransaction, error) {
	const query = `
SELECT id, amount, fee, net, currency, type, source_id, payout_id, created_at, available_on, description
FROM stripe_balance_transactions WHERE payout_id = ? ORDER BY created_at ASC`
	rows, err := q.db.QueryContext(ctx, query, payoutID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var txns []StripeBalanceTransaction
	for rows.Next() {
		bt := StripeBalanceTransaction{}
		if err := rows.Scan(&bt.ID, &bt.Amount, &bt.Fee, &bt.Net, &bt.Currency, &bt.Type,
			&bt.SourceID, &bt.PayoutID, &bt.CreatedAt, &bt.AvailableOn, &bt.Description); err != nil {
			return nil, err
		}
		txns = append(txns, bt)
	}
	return txns, rows.Err()
}

func (q *Queries) GetBalanceTransactionBySource(ctx context.Context, sourceID string) (*StripeBalanceTransaction, error) {
	const query = `
SELECT id, amount, fee, net, currency, type, source_id, payout_id, created_at, available_on, description
FROM stripe_balance_transactions WHERE source_id = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, sourceID)
	bt := &StripeBalanceTransaction{}
	err := row.Scan(&bt.ID, &bt.Amount, &bt.Fee, &bt.Net, &bt.Currency, &bt.Type,
		&bt.SourceID, &bt.PayoutID, &bt.CreatedAt, &bt.AvailableOn, &bt.Description)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return bt, err
}

// ── Payment Intents ───────────────────────────────────────────────────────────

func (q *Queries) UpsertStripePaymentIntent(ctx context.Context, pi StripePaymentIntent) error {
	const query = `
INSERT INTO stripe_payment_intents
    (id, stripe_customer_id, amount, currency, status, description, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET status = excluded.status`
	_, err := q.db.ExecContext(ctx, query,
		pi.ID, pi.StripeCustomerID, pi.Amount, pi.Currency,
		pi.Status, pi.Description, pi.Metadata, pi.CreatedAt)
	return err
}

// ── Sync State ────────────────────────────────────────────────────────────────

func (q *Queries) GetSyncState(ctx context.Context, entityType string) (*SyncState, error) {
	const query = `
SELECT id, entity_type, last_synced_id, last_synced_at, status
FROM sync_state WHERE entity_type = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, entityType)
	s := &SyncState{}
	err := row.Scan(&s.ID, &s.EntityType, &s.LastSyncedID, &s.LastSyncedAt, &s.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (q *Queries) UpdateSyncState(ctx context.Context, lastSyncedID string, lastSyncedAt int64, status, entityType string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE sync_state SET last_synced_id = ?, last_synced_at = ?, status = ? WHERE entity_type = ?`,
		nullStr(lastSyncedID), lastSyncedAt, status, entityType)
	return err
}

func (q *Queries) SetSyncStatus(ctx context.Context, status, entityType string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE sync_state SET status = ? WHERE entity_type = ?`, status, entityType)
	return err
}

func (q *Queries) ListSyncStates(ctx context.Context) ([]SyncState, error) {
	const query = `SELECT id, entity_type, last_synced_id, last_synced_at, status FROM sync_state ORDER BY entity_type`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var states []SyncState
	for rows.Next() {
		s := SyncState{}
		if err := rows.Scan(&s.ID, &s.EntityType, &s.LastSyncedID, &s.LastSyncedAt, &s.Status); err != nil {
			return nil, err
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

// ── Sync Log ──────────────────────────────────────────────────────────────────

func (q *Queries) InsertSyncLog(ctx context.Context, entityType, entityID, action, details string) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO sync_log (entity_type, entity_id, action, details) VALUES (?, ?, ?, ?)`,
		entityType, entityID, action, nullStr(details))
	return err
}

func (q *Queries) ListRecentSyncLogs(ctx context.Context, limit int64) ([]SyncLog, error) {
	const query = `
SELECT id, entity_type, entity_id, action, details, created_at
FROM sync_log ORDER BY created_at DESC LIMIT ?`
	rows, err := q.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []SyncLog
	for rows.Next() {
		l := SyncLog{}
		if err := rows.Scan(&l.ID, &l.EntityType, &l.EntityID, &l.Action, &l.Details, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// ── Fortnox Vouchers ──────────────────────────────────────────────────────────

// InsertPendingFortnoxVoucher reserves a local row before the Fortnox API call.
// Uses INSERT OR IGNORE so a concurrent runner silently skips if the row exists.
func (q *Queries) InsertPendingFortnoxVoucher(ctx context.Context, v FortnoxVoucher) error {
	const query = `
INSERT OR IGNORE INTO fortnox_vouchers
    (fortnox_voucher_series, voucher_date, description,
     source_type, source_id, total_debit, total_credit)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := q.db.ExecContext(ctx, query,
		v.FortnoxVoucherSeries, v.VoucherDate, v.Description,
		v.SourceType, v.SourceID, v.TotalDebit, v.TotalCredit)
	return err
}

// ConfirmFortnoxVoucher writes the voucher number and raw response after a successful Fortnox POST.
func (q *Queries) ConfirmFortnoxVoucher(ctx context.Context, voucherNumber, responseData, sourceType, sourceID string) error {
	const query = `
UPDATE fortnox_vouchers
SET fortnox_voucher_number = ?, response_data = ?
WHERE source_type = ? AND source_id = ? AND fortnox_voucher_number IS NULL`
	_, err := q.db.ExecContext(ctx, query, voucherNumber, responseData, sourceType, sourceID)
	return err
}

// InsertFortnoxVoucher is kept for compatibility but internally uses the two-phase pattern.
func (q *Queries) InsertFortnoxVoucher(ctx context.Context, v FortnoxVoucher) (*FortnoxVoucher, error) {
	if err := q.InsertPendingFortnoxVoucher(ctx, v); err != nil {
		return nil, err
	}
	if v.FortnoxVoucherNumber.Valid {
		if err := q.ConfirmFortnoxVoucher(ctx,
			v.FortnoxVoucherNumber.String,
			v.ResponseData.String,
			v.SourceType, v.SourceID); err != nil {
			return nil, err
		}
	}
	return q.GetFortnoxVoucherBySource(ctx, v.SourceType, v.SourceID)
}

func (q *Queries) GetFortnoxVoucherBySource(ctx context.Context, sourceType, sourceID string) (*FortnoxVoucher, error) {
	const query = `
SELECT id, fortnox_voucher_number, fortnox_voucher_series, voucher_date, description,
    source_type, source_id, total_debit, total_credit, response_data, created_at
FROM fortnox_vouchers WHERE source_type = ? AND source_id = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, sourceType, sourceID)
	v := &FortnoxVoucher{}
	err := row.Scan(&v.ID, &v.FortnoxVoucherNumber, &v.FortnoxVoucherSeries, &v.VoucherDate,
		&v.Description, &v.SourceType, &v.SourceID, &v.TotalDebit, &v.TotalCredit,
		&v.ResponseData, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func (q *Queries) ListFortnoxVouchers(ctx context.Context, limit, offset int64) ([]FortnoxVoucher, error) {
	const query = `
SELECT id, fortnox_voucher_number, fortnox_voucher_series, voucher_date, description,
    source_type, source_id, total_debit, total_credit, response_data, created_at
FROM fortnox_vouchers ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := q.db.QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vouchers []FortnoxVoucher
	for rows.Next() {
		v := FortnoxVoucher{}
		if err := rows.Scan(&v.ID, &v.FortnoxVoucherNumber, &v.FortnoxVoucherSeries,
			&v.VoucherDate, &v.Description, &v.SourceType, &v.SourceID,
			&v.TotalDebit, &v.TotalCredit, &v.ResponseData, &v.CreatedAt); err != nil {
			return nil, err
		}
		vouchers = append(vouchers, v)
	}
	return vouchers, rows.Err()
}

func (q *Queries) CountFortnoxVouchers(ctx context.Context) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fortnox_vouchers`).Scan(&count)
	return count, err
}

// ListPendingFortnoxVouchers returns vouchers that were reserved locally but
// never confirmed — i.e. the Fortnox API call never completed successfully.
// These are surfaced in the UI for manual review; they are NOT retried automatically.
func (q *Queries) ListPendingFortnoxVouchers(ctx context.Context) ([]FortnoxVoucher, error) {
	const query = `
SELECT id, fortnox_voucher_number, fortnox_voucher_series, voucher_date, description,
    source_type, source_id, total_debit, total_credit, response_data, created_at
FROM fortnox_vouchers WHERE fortnox_voucher_number IS NULL ORDER BY created_at DESC`
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vouchers []FortnoxVoucher
	for rows.Next() {
		v := FortnoxVoucher{}
		if err := rows.Scan(&v.ID, &v.FortnoxVoucherNumber, &v.FortnoxVoucherSeries,
			&v.VoucherDate, &v.Description, &v.SourceType, &v.SourceID,
			&v.TotalDebit, &v.TotalCredit, &v.ResponseData, &v.CreatedAt); err != nil {
			return nil, err
		}
		vouchers = append(vouchers, v)
	}
	return vouchers, rows.Err()
}

// DeleteFortnoxVoucher removes a pending voucher row so the source can be retried.
func (q *Queries) DeleteFortnoxVoucher(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM fortnox_vouchers WHERE id = ?`, id)
	return err
}

func (q *Queries) CountPendingFortnoxVouchers(ctx context.Context) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM fortnox_vouchers WHERE fortnox_voucher_number IS NULL`).Scan(&count)
	return count, err
}

// ── Settings ──────────────────────────────────────────────────────────────────

func (q *Queries) GetSetting(ctx context.Context, key string) (*Setting, error) {
	const query = `SELECT key, value, encrypted, updated_at FROM settings WHERE key = ? LIMIT 1`
	row := q.db.QueryRowContext(ctx, query, key)
	s := &Setting{}
	err := row.Scan(&s.Key, &s.Value, &s.Encrypted, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (q *Queries) UpsertSetting(ctx context.Context, key, value string, encrypted int64) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, encrypted, updated_at)
         VALUES (?, ?, ?, CURRENT_TIMESTAMP)
         ON CONFLICT(key) DO UPDATE SET
             value     = excluded.value,
             encrypted = excluded.encrypted,
             updated_at = CURRENT_TIMESTAMP`,
		key, value, encrypted)
	return err
}

func (q *Queries) ListSettings(ctx context.Context) ([]Setting, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT key, value, encrypted, updated_at FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var settings []Setting
	for rows.Next() {
		s := Setting{}
		if err := rows.Scan(&s.Key, &s.Value, &s.Encrypted, &s.UpdatedAt); err != nil {
			return nil, err
		}
		settings = append(settings, s)
	}
	return settings, rows.Err()
}

func (q *Queries) DeleteSetting(ctx context.Context, key string) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}
