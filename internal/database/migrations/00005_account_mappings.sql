-- +goose Up

CREATE TABLE account_mappings (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    kontotyp  TEXT NOT NULL,
    matchtyp  TEXT NOT NULL,
    matchkod  TEXT NOT NULL,
    konto     TEXT NOT NULL,
    momssats  REAL
);

CREATE UNIQUE INDEX idx_account_mappings_lookup ON account_mappings(kontotyp, matchkod);

-- Pre-seed defaults matching Swedish BAS-kontoplan
INSERT INTO account_mappings (kontotyp, matchtyp, matchkod, konto, momssats) VALUES
    ('Intäktskonto',      'Landskod', 'SE',  '3010', 25.0),
    ('Intäktskonto',      'Landskod', 'EU',  '3007', 25.0),
    ('Intäktskonto',      'Landskod', 'WO',  '3008', 25.0),
    ('Avstämningskonto',  'Valuta',   'SEK', '1521', NULL),
    ('Utbetalningskonto', 'Valuta',   'SEK', '1930', NULL),
    ('BetalväxelAvgift',  'Valuta',   'SEK', '6065', 25.0),
    ('OmvändMomsDebet',   'Valuta',   'SEK', '2645', NULL),
    ('OmvändMomsKredit',  'Valuta',   'SEK', '2614', NULL);

-- Track Fortnox invoice per charge (replaces fortnox_vouchers for charges)
ALTER TABLE stripe_charges ADD COLUMN fortnox_invoice_number TEXT;

-- Mark charges already synced via old voucher flow as LEGACY to skip re-processing
UPDATE stripe_charges
SET fortnox_invoice_number = 'LEGACY'
WHERE id IN (
    SELECT source_id FROM fortnox_vouchers WHERE source_type = 'charge'
);

-- +goose Down
DROP INDEX IF EXISTS idx_account_mappings_lookup;
DROP TABLE IF EXISTS account_mappings;
