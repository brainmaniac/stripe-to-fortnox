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
| Charge voucher — Swedish / unknown country | `internal/fortnox/voucher_test.go`: `TestChargeVoucherSE`, `TestSEVATSplit`, `TestVoucherBalancedCharge` |
| Charge voucher — EU and international | `internal/fortnox/voucher_test.go`: `TestChargeVoucherEU`, `TestChargeVoucherWO`, `TestVoucherBalancedCharge` |
| Payout voucher | `internal/fortnox/voucher_test.go`: `TestPayoutVoucherBalance`, `TestPayoutVoucherAccounts` |
| Fee voucher with reverse VAT | `internal/fortnox/voucher_test.go`: `TestFeeVoucherReverseVAT` |
| All vouchers are balanced | `internal/fortnox/voucher_test.go`: `TestVoucherBalancedCharge`, `TestPayoutVoucherBalance`, `TestPostVoucherRejectsImbalanced` |
| Stripe data is idempotent (UPSERT) | `internal/db/queries_test.go`: `TestUpsertStripeChargeIdempotent`, `TestUpsertStripePayoutIdempotent`, `TestUpsertStripeCustomerIdempotent`, `TestUpsertStripeBalanceTransactionIdempotent`, `TestUpsertChargePreservesBillingCountry` |
| Fortnox vouchers are idempotent (two-phase write) | `internal/fortnox/voucher_test.go`: `TestChargeVoucherIdempotentConfirmed`, `TestChargeVoucherFailedFortnoxLeavesPendingRow`; `internal/db/queries_test.go`: `TestInsertPendingVoucherIdempotent`, `TestListUnsyncedChargesPendingAndConfirmed`, `TestListUnsyncedPayoutsPendingAndConfirmed` |
| Account 1521 as clearing account | `internal/fortnox/voucher_test.go`: `TestStripeClearingAccountIsShared`, `TestPayoutVoucherAccounts` |

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
