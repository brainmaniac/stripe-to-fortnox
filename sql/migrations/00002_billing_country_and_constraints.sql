-- +goose Up

-- Add billing country to charges for country-based revenue account routing.
ALTER TABLE stripe_charges ADD COLUMN billing_country TEXT;

-- Ensure each Stripe source object maps to at most one local voucher record.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_fv_source ON fortnox_vouchers(source_type, source_id);

-- +goose Down
DROP INDEX IF EXISTS uniq_fv_source;
