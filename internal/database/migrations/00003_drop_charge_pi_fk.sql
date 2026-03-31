-- +goose Up
-- SQLite cannot drop a single foreign key; recreate the table without the
-- payment_intent_id FK (payment intents are not synced or used by the app).

PRAGMA foreign_keys = OFF;

CREATE TABLE stripe_charges_new (
    id TEXT PRIMARY KEY,
    payment_intent_id TEXT,
    amount INTEGER NOT NULL,
    amount_captured INTEGER NOT NULL DEFAULT 0,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    balance_transaction_id TEXT,
    customer_id TEXT,
    description TEXT,
    metadata TEXT,
    created_at INTEGER NOT NULL,
    billing_country TEXT,
    FOREIGN KEY (customer_id) REFERENCES stripe_customers(id)
);

INSERT INTO stripe_charges_new
SELECT id, payment_intent_id, amount, amount_captured, currency, status,
       balance_transaction_id, customer_id, description, metadata, created_at,
       billing_country
FROM stripe_charges;

DROP TABLE stripe_charges;
ALTER TABLE stripe_charges_new RENAME TO stripe_charges;

CREATE INDEX IF NOT EXISTS idx_charges_pi ON stripe_charges(payment_intent_id);
CREATE INDEX IF NOT EXISTS idx_charges_customer ON stripe_charges(customer_id);
CREATE INDEX IF NOT EXISTS idx_charges_bal_txn ON stripe_charges(balance_transaction_id);
CREATE INDEX IF NOT EXISTS idx_charges_status ON stripe_charges(status);
CREATE INDEX IF NOT EXISTS idx_charges_created ON stripe_charges(created_at);

PRAGMA foreign_keys = ON;

-- +goose Down
-- Restore the FK (charges created without a payment_intent in the DB will fail to revert)
PRAGMA foreign_keys = OFF;

CREATE TABLE stripe_charges_old (
    id TEXT PRIMARY KEY,
    payment_intent_id TEXT,
    amount INTEGER NOT NULL,
    amount_captured INTEGER NOT NULL DEFAULT 0,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    balance_transaction_id TEXT,
    customer_id TEXT,
    description TEXT,
    metadata TEXT,
    created_at INTEGER NOT NULL,
    billing_country TEXT,
    FOREIGN KEY (payment_intent_id) REFERENCES stripe_payment_intents(id),
    FOREIGN KEY (customer_id) REFERENCES stripe_customers(id)
);

INSERT INTO stripe_charges_old SELECT * FROM stripe_charges;
DROP TABLE stripe_charges;
ALTER TABLE stripe_charges_old RENAME TO stripe_charges;

PRAGMA foreign_keys = ON;
