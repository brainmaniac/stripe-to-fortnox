-- +goose Up
ALTER TABLE stripe_charges ADD COLUMN billing_country TEXT;

-- Unique constraint enables INSERT OR IGNORE idempotency in InsertPendingFortnoxVoucher.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_fv_source ON fortnox_vouchers(source_type, source_id);

-- +goose Down
DROP INDEX IF EXISTS uniq_fv_source;
