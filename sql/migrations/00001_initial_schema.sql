-- +goose Up

CREATE TABLE IF NOT EXISTS stripe_customers (
    id TEXT PRIMARY KEY,
    email TEXT,
    name TEXT,
    metadata TEXT,
    created_at INTEGER NOT NULL,
    fortnox_customer_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_customers_email ON stripe_customers(email);
CREATE INDEX IF NOT EXISTS idx_customers_fortnox ON stripe_customers(fortnox_customer_id);

CREATE TABLE IF NOT EXISTS stripe_payment_intents (
    id TEXT PRIMARY KEY,
    stripe_customer_id TEXT,
    amount INTEGER NOT NULL,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    description TEXT,
    metadata TEXT,
    created_at INTEGER NOT NULL,
    synced_at DATETIME,
    fortnox_voucher_id INTEGER,
    FOREIGN KEY (stripe_customer_id) REFERENCES stripe_customers(id)
);
CREATE INDEX IF NOT EXISTS idx_pi_customer ON stripe_payment_intents(stripe_customer_id);
CREATE INDEX IF NOT EXISTS idx_pi_status ON stripe_payment_intents(status);
CREATE INDEX IF NOT EXISTS idx_pi_created ON stripe_payment_intents(created_at);

CREATE TABLE IF NOT EXISTS stripe_charges (
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
    FOREIGN KEY (payment_intent_id) REFERENCES stripe_payment_intents(id),
    FOREIGN KEY (customer_id) REFERENCES stripe_customers(id)
);
CREATE INDEX IF NOT EXISTS idx_charges_pi ON stripe_charges(payment_intent_id);
CREATE INDEX IF NOT EXISTS idx_charges_customer ON stripe_charges(customer_id);
CREATE INDEX IF NOT EXISTS idx_charges_bal_txn ON stripe_charges(balance_transaction_id);
CREATE INDEX IF NOT EXISTS idx_charges_status ON stripe_charges(status);
CREATE INDEX IF NOT EXISTS idx_charges_created ON stripe_charges(created_at);

CREATE TABLE IF NOT EXISTS stripe_payouts (
    id TEXT PRIMARY KEY,
    amount INTEGER NOT NULL,
    currency TEXT NOT NULL,
    arrival_date INTEGER NOT NULL,
    status TEXT NOT NULL,
    description TEXT,
    created_at INTEGER NOT NULL,
    synced_at DATETIME,
    fortnox_voucher_id INTEGER
);
CREATE INDEX IF NOT EXISTS idx_payouts_status ON stripe_payouts(status);
CREATE INDEX IF NOT EXISTS idx_payouts_created ON stripe_payouts(created_at);

CREATE TABLE IF NOT EXISTS stripe_balance_transactions (
    id TEXT PRIMARY KEY,
    amount INTEGER NOT NULL,
    fee INTEGER NOT NULL DEFAULT 0,
    net INTEGER NOT NULL,
    currency TEXT NOT NULL,
    type TEXT NOT NULL,
    source_id TEXT,
    payout_id TEXT,
    created_at INTEGER NOT NULL,
    available_on INTEGER NOT NULL,
    description TEXT,
    FOREIGN KEY (payout_id) REFERENCES stripe_payouts(id)
);
CREATE INDEX IF NOT EXISTS idx_bt_type ON stripe_balance_transactions(type);
CREATE INDEX IF NOT EXISTS idx_bt_source ON stripe_balance_transactions(source_id);
CREATE INDEX IF NOT EXISTS idx_bt_payout ON stripe_balance_transactions(payout_id);
CREATE INDEX IF NOT EXISTS idx_bt_created ON stripe_balance_transactions(created_at);

CREATE TABLE IF NOT EXISTS sync_state (
    id INTEGER PRIMARY KEY,
    entity_type TEXT NOT NULL UNIQUE,
    last_synced_id TEXT,
    last_synced_at INTEGER,
    status TEXT NOT NULL DEFAULT 'idle'
);

CREATE TABLE IF NOT EXISTS sync_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    action TEXT NOT NULL,
    details TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sync_log_entity ON sync_log(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_sync_log_action ON sync_log(action);
CREATE INDEX IF NOT EXISTS idx_sync_log_created ON sync_log(created_at);

CREATE TABLE IF NOT EXISTS fortnox_vouchers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    fortnox_voucher_number TEXT,
    fortnox_voucher_series TEXT NOT NULL DEFAULT 'A',
    voucher_date TEXT NOT NULL,
    description TEXT,
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    total_debit INTEGER NOT NULL DEFAULT 0,
    total_credit INTEGER NOT NULL DEFAULT 0,
    response_data TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_fv_source ON fortnox_vouchers(source_type, source_id);
CREATE INDEX IF NOT EXISTS idx_fv_date ON fortnox_vouchers(voucher_date);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    encrypted INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('charges', 'idle');
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('payouts', 'idle');
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('balance_transactions', 'idle');
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('customers', 'idle');
INSERT OR IGNORE INTO sync_state (entity_type, status) VALUES ('payment_intents', 'idle');

-- +goose Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS fortnox_vouchers;
DROP TABLE IF EXISTS sync_log;
DROP TABLE IF EXISTS sync_state;
DROP TABLE IF EXISTS stripe_balance_transactions;
DROP TABLE IF EXISTS stripe_payouts;
DROP TABLE IF EXISTS stripe_charges;
DROP TABLE IF EXISTS stripe_payment_intents;
DROP TABLE IF EXISTS stripe_customers;
