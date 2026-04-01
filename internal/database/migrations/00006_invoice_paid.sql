-- +goose Up
ALTER TABLE stripe_charges ADD COLUMN fortnox_invoice_paid INTEGER NOT NULL DEFAULT 0;

-- +goose Down
-- SQLite does not support DROP COLUMN reliably; leave as-is
