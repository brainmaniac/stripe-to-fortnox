# stripe-fortnox-sync

A self-hosted Go web application that syncs Stripe payment data into Fortnox (Swedish accounting software) as vouchers.

## Features

- Pulls charges, payouts, balance transactions, and customers from Stripe
- Maps them to Swedish BAS-kontoplan accounting entries
- Creates vouchers (verifikationer) in Fortnox via REST API
- Web dashboard to monitor sync status, review vouchers, and trigger manual syncs

## Walkthrough

This section describes what the app actually does, end to end. Each bullet is covered by one or more automated tests — if the tests pass, the behavior described here is working correctly. See `CLAUDE.md` for the rule that keeps this list and the tests in sync.

- **Stripe data pull (incremental)**: When "Sync from Stripe" is triggered, the app fetches customers, charges, and payouts from Stripe's API. It only pulls records created since the last successful sync (minus a 60-second overlap to handle clock skew), so the pull gets faster over time as history accumulates.

- **Balance transaction sync**: When a payout reaches "paid" status — either detected during a sync or via a real-time webhook — the app immediately fetches all balance transactions settled in that payout from Stripe. These transactions detail the individual charges that make up the payout amount and are stored locally for reference.

- **Real-time updates via webhook**: Stripe sends event notifications to `/webhook/stripe`. The app processes `charge.succeeded`, `charge.updated`, `charge.refunded`, `payout.paid`, `payout.updated`, `payout.reconciliation_completed`, `customer.created`, and `customer.updated` in real time, keeping the local database current without waiting for the next manual sync.

- **Revenue account routing by billing country**: Each charge carries a billing country from Stripe's BillingDetails field. The app uses this to pick the correct Fortnox revenue account: Sweden (SE or unknown) → 3010, EU member states → 3007, rest of world → 3008.

- **Charge voucher — Swedish / unknown country**: For each unsynced succeeded charge with a Swedish or unknown billing country, a voucher is created with 25% VAT split out: debit 1521 (Stripe clearing), credit 3010 (net revenue, 80%) + credit 2611 (output VAT 25%, 20%).

- **Charge voucher — EU and international**: For EU and international charges, no Swedish VAT is applied. The full charge amount goes to the revenue account: debit 1521, credit 3007 (EU) or 3008 (rest of world).

- **Payout voucher**: When a payout is pushed to Fortnox, a voucher is created that records the money moving from Stripe to the bank: debit 1930 (bank account), credit 1521 (Stripe clearing).

- **Fee voucher with reverse VAT (omvänd moms)**: For each Stripe processing fee, a separate voucher is created. Because Stripe Ltd is an Irish EU company, reverse VAT (omvänd skattskyldighet) applies: debit 6065 (payment fee) + debit 2645 (reverse VAT debit), credit 2614 (reverse VAT credit) + credit 1521 (Stripe clearing). The reversal entries cancel each other — only the fee cost hits the P&L.

- **All vouchers are balanced (double-entry)**: Every voucher's debit total equals its credit total. The app validates this before calling Fortnox and rejects any imbalanced voucher.

- **Stripe data is idempotent (UPSERT)**: All Stripe data — charges, payouts, customers, balance transactions — is written with `INSERT … ON CONFLICT DO UPDATE`. Re-running a sync or receiving a duplicate webhook event never creates duplicate records.

- **Fortnox vouchers are idempotent (two-phase write)**: Before calling Fortnox, the app inserts a pending row locally using `INSERT OR IGNORE`. After a successful Fortnox response, the row is updated with the Fortnox voucher number. If the process crashes between the API call and the database update, the pending row remains — it is surfaced on the Sync page as a warning. The charge or payout is **not** retried automatically; it is excluded from the unsynced list so you can check Fortnox manually and decide whether to void a duplicate.

- **Account 1521 as the Stripe clearing account**: Account 1521 bridges charges and payouts. Every sale credits 1521 (money owed by Stripe), and every payout debits 1521 (money received from Stripe). Its running balance matches the Stripe dashboard balance at all times.

## Quick Start

### 1. Copy and configure environment

```sh
cp .env.example .env
```

Edit `.env` and fill in:
- `ADMIN_PASSWORD_HASH` — bcrypt hash of your admin password
- `SESSION_SECRET` — random 32-byte hex string
- `STRIPE_API_KEY` — your Stripe API key
- `STRIPE_WEBHOOK_SECRET` — from Stripe webhook settings
- `FORTNOX_CLIENT_ID` / `FORTNOX_CLIENT_SECRET` — from Fortnox developer portal
- `BASE_URL` — public URL of this app (for OAuth callbacks)

Generate a password hash:
```sh
htpasswd -bnBC 10 "" 'yourpassword' | tr -d ':\n' | sed 's/$2y/$2a/'
```

Generate a session secret:
```sh
openssl rand -hex 32
```

### 2. Run with Docker

```sh
docker compose up -d
```

Open http://localhost:8080 and log in with your password.

### 3. Connect Fortnox

Go to **Inställningar** and click **Anslut Fortnox** to start the OAuth2 flow.

### 4. Sync data

- Go to **Synkronisering** and click **Hämta från Stripe** to pull the latest data
- Click **Skicka till Fortnox** to create accounting vouchers

## Local Development

Requires: Go 1.23+, [templ CLI v0.2.476](https://templ.guide/quick-start/installation)

```sh
templ generate
go run ./cmd/server
```

Run tests:

```sh
go test ./...
```

## Accounting Model

Uses Swedish BAS-kontoplan accounts (configurable in Inställningar):

| Account | Description |
|---------|-------------|
| 1521 | Avstämningskonto Stripe (clearing) |
| 1930 | Företagskonto/bank |
| 3010 | Intäkter Sverige (25% moms) |
| 3007 | Intäkter EU (omvänd moms) |
| 3008 | Intäkter utanför EU/export |
| 2611 | Utgående moms 25% |
| 6065 | Betalväxelavgift (Stripe fees) |
| 2645 | Omvänd moms debet |
| 2614 | Omvänd moms kredit |

Account 1521 acts as the clearing account — its running balance should equal the Stripe dashboard balance.

## Stripe Webhook

Point your Stripe webhook to `https://your-domain/webhook/stripe` and enable these events:

- `charge.succeeded`, `charge.updated`, `charge.refunded`
- `payout.paid`, `payout.updated`, `payout.reconciliation_completed`
- `customer.created`, `customer.updated`
