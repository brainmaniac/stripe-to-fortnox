-- +goose Up
ALTER TABLE stripe_customers RENAME COLUMN metadata TO country;

-- +goose Down
ALTER TABLE stripe_customers RENAME COLUMN country TO metadata;
