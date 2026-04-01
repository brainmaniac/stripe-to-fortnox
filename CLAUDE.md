# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Interaction protocol

- **`DO:` prefix** — the user wants you to write code and make changes to the codebase. Only make changes when this prefix is present.
- **No `DO:` prefix** — the user is asking for information, explanation, or discussion. Answer only; do not touch files.
- **Ambiguous prompt** — if a prompt looks like a command but lacks `DO:`, ask: *"Did you mean this as a DO: command?"* and wait for confirmation before acting.

## Commands

```bash
# First-time setup (generates .env interactively)
go run ./cmd/setup

# Generate templ templates and sqlc code (required before build after template/query changes)
make generate          # runs: templ generate && sqlc generate

# Run tests
go test ./...

# Run a single test
go test ./internal/fortnox/... -run TestChargeVoucherSE

# Build binary
make build             # output: bin/server

# Run server (development)
go run ./cmd/server

# Watch mode (two terminals)
# Terminal 1: templ generate --watch
# Terminal 2: go run ./cmd/server

# Docker
docker compose up -d
docker compose logs -f
```

## Architecture

This is a self-hosted Go web app that syncs Stripe payment data to Fortnox (Swedish accounting) as double-entry vouchers.

**Request flow:** `cmd/server/main.go` wires all dependencies → chi router → `internal/handler/` → services in `internal/stripe/`, `internal/fortnox/`, `internal/db/`

**Key layers:**
- `internal/config/` — env-driven config loaded at startup
- `internal/database/` — opens SQLite, sets pragmas (WAL, FK, 32MB cache), runs embedded goose migrations; max 1 open connection
- `internal/db/` — **sqlc-generated** query code (do not edit manually); source queries live in `sql/queries/`
- `internal/stripe/` — incremental sync and webhook processing; converts Stripe objects to DB rows
- `internal/fortnox/` — voucher creation with Swedish accounting rules (VAT, BAS accounts, reverse VAT for fees); two-phase write: INSERT pending row → POST to Fortnox → UPDATE to confirmed
- `internal/views/` — Templ HTML templates (`.go` files are generated; edit `.templ` source files)
- `internal/handler/` — HTTP handlers for dashboard, sync, settings, webhook, auth

**Accounting rules baked in:**
- Account 1521 = Stripe clearing account (shared across charge/payout vouchers)
- Revenue routing: SE/unknown → 3010, EU → 3007, RoW → 3008
- SE/unknown charges get 25% VAT split; EU charges are VAT-exempt
- Stripe processing fees use reverse VAT (omvänd moms, 25%)

**sqlc workflow:** edit `sql/queries/*.sql` → run `sqlc generate` → commit generated `internal/db/queries.sql.go`

**Templ workflow:** edit `internal/views/*.templ` → run `templ generate` → commit generated `*_templ.go` files

---

# Agent Instructions

## Walkthrough ↔ Tests coupling (MANDATORY)

The `## Walkthrough` section of `README.md` is the canonical description of what this application does. Each bullet in that section **must** be covered by one or more automated tests. This coupling is intentional: if the Walkthrough says "X happens", there must be a test that fails if X stops happening.

**Rules that every agent must follow:**

1. **When you change app behavior**, update the matching bullet in the Walkthrough AND the matching test(s). Do not leave them out of sync.

2. **When you add new behavior**, add a Walkthrough bullet describing it in plain language AND write a test that covers it.

3. **When you remove behavior**, remove the Walkthrough bullet AND the test(s) that covered it.

4. **After any non-trivial change**, run `go test ./...` and confirm all tests pass. A green test suite means the Walkthrough is accurate.

5. **Do not change the Walkthrough without changing tests, and vice versa.** The goal is that a user reading the Walkthrough can trust it is machine-verified.

### Current Walkthrough → Test mapping

| Walkthrough bullet | Test(s) |
|---|---|
| Stripe data pull (incremental) | `internal/stripe/sync_test.go`: `TestChargeCreatedAtPreserved` |
| Balance transaction sync | `internal/db/queries_test.go`: `TestBalanceTransactionsPerPayout` |
| Real-time updates via webhook | `internal/stripe/sync_test.go`: `TestChargeFromStripeRefundedStatus`, `TestChargeFromStripeFieldMapping` |
| Revenue account routing by billing country | `internal/fortnox/voucher_test.go`: `TestRevenueAccountRouting` |
| Charge → Fortnox invoice (B-series via /3/invoices) | `internal/fortnox/voucher_test.go`: `TestRevenueAccountRouting` |
| Charge invoice idempotent via fortnox_invoice_number | `internal/db/queries_test.go`: `TestListUnsyncedChargesPendingAndConfirmed` |
| Payout voucher (C-series) + invoicepayment per charge + fee voucher | `internal/fortnox/voucher_test.go`: `TestPayoutVoucherBalance`, `TestPayoutVoucherAccounts`, `TestFeeVoucherReverseVAT` |
| All vouchers are balanced | `internal/fortnox/voucher_test.go`: `TestPayoutVoucherBalance`, `TestPostVoucherRejectsImbalanced` |
| Stripe data is idempotent (UPSERT) | `internal/db/queries_test.go`: `TestUpsertStripeChargeIdempotent`, `TestUpsertStripePayoutIdempotent`, `TestUpsertStripeCustomerIdempotent`, `TestUpsertStripeBalanceTransactionIdempotent`, `TestUpsertChargePreservesBillingCountry` |
| Payout vouchers are idempotent (two-phase write) | `internal/fortnox/voucher_test.go`: `TestChargeVoucherIdempotentConfirmed`, `TestChargeVoucherFailedFortnoxLeavesPendingRow`; `internal/db/queries_test.go`: `TestInsertPendingVoucherIdempotent`, `TestListUnsyncedPayoutsPendingAndConfirmed` |
| Account 1521 as clearing account | `internal/fortnox/voucher_test.go`: `TestStripeClearingAccountIsShared`, `TestPayoutVoucherAccounts` |
| Account mappings configurable per country group | `internal/db/queries_test.go`: covers via `TestListUnsyncedChargesPendingAndConfirmed` (uses migrated seed data) |

## Technology notes

- **Go 1.23**, module path `stripe-fortnox-sync`
- **SQLite** via `modernc.org/sqlite` (pure Go, driver name `"sqlite"`)
- **Migrations** via `pressly/goose/v3`, embedded in `internal/database/migrations/`, dialect `"sqlite3"`
- **Templ v0.2.476** for HTML templates — run `templ generate` before `go build`
- **Stripe** `stripe-go/v84`, **Fortnox** REST API at `https://api.fortnox.se/3/`
- **Session management** via `alexedwards/scs/v2`

## Test conventions

- Tests in `internal/db/` use `package db_test` (external test package) to avoid import cycles
- `internal/testutil.NewTestDB(t)` opens an in-memory SQLite with all migrations applied
- Fortnox API calls in tests use `mockPoster` (defined in `internal/fortnox/voucher_test.go`)
- No Stripe API calls are made in tests — `chargeFromStripe` is tested with hand-crafted structs

## Known limitations

- **Fortnox has no idempotency keys**: If the process crashes after a successful Fortnox POST but before the local `ConfirmFortnoxVoucher` UPDATE, a duplicate voucher may exist in Fortnox. The app handles this by surfacing the pending row on the Sync page and NOT retrying automatically. The user must check Fortnox manually and void any duplicate. This is a Fortnox API limitation (no idempotency key support as of 2026).
