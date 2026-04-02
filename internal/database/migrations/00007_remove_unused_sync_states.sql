-- +goose Up
DELETE FROM sync_state WHERE entity_type IN ('payment_intents', 'balance_transactions');

-- +goose Down
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('balance_transactions', 'idle');
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('payment_intents', 'idle');
