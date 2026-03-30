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

## Getting Started

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and Docker Compose
- Go 1.23+ (only needed if running outside Docker or running setup wizard)

### 1. Run the setup wizard

```sh
go run ./cmd/setup
```

The wizard walks you through every required key — Stripe, Fortnox, admin password — and writes a `.env` file. The admin password is hashed with bcrypt and the session secret is generated automatically. You only need to run it once.

### 2. Start the app

```sh
docker compose up -d
```

Open http://localhost:8080 and log in with the password you set in step 1.

### 3. Connect Fortnox

Go to **Inställningar** and click **Anslut Fortnox** to complete the OAuth2 flow. You'll be redirected back automatically.

### 4. Pull data and create vouchers

1. Go to **Synkronisering** → **Hämta från Stripe** to pull charges, payouts, and customers
2. Click **Skicka till Fortnox** to create accounting vouchers from the synced data

### 5. Set up Stripe webhook (optional, for real-time updates)

Point your Stripe webhook to `https://your-domain/webhook/stripe` and enable the events listed in the [Stripe Webhook](#stripe-webhook) section below.

---

## Deploying to a VPS

The included GitHub Actions workflow (`.github/workflows/deploy.yml`) SSHes to your server on every push to `main`, clones the repo if needed, and rebuilds the Docker image in place.

### Server prerequisites

On your Debian VPS, install Git, Docker and Docker Compose:

```sh
sudo apt-get update && sudo apt-get install -y git
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER   # allow your deploy user to run docker
```

### One-time server setup

SSH into your VPS and run the setup wizard to create the `.env` file:

```sh
git clone <your-repo-url> /opt/stripe-fortnox-sync
cd /opt/stripe-fortnox-sync
go run ./cmd/setup       # generates .env — only needed once
```

The `.env` file is never committed and will survive future `git pull`s.

### GitHub repository secrets

Add these secrets under **Settings → Secrets → Actions** in your GitHub repo:

| Secret | Example |
|--------|---------|
| `VPS_HOST` | `123.456.78.90` or `myserver.example.com` |
| `VPS_USER` | `deploy` |
| `VPS_SSH_KEY` | `-----BEGIN OPENSSH PRIVATE KEY-----\nAAA...` |
| `VPS_DEPLOY_PATH` | `/opt/stripe-fortnox-sync` |
| `VPS_REPO_URL` | `git@github.com:youruser/stripe-to-fortnox.git` |

For `VPS_SSH_KEY`, generate a dedicated key pair and add the public key to `~/.ssh/authorized_keys` on the VPS:

```sh
ssh-keygen -t ed25519 -C "github-actions-deploy" -f deploy_key
# Add deploy_key.pub to ~/.ssh/authorized_keys on the VPS
# Paste deploy_key (private) as the VPS_SSH_KEY secret
```

If the repository is private, also add the VPS's SSH public key as a [deploy key](https://docs.github.com/en/authentication/connecting-to-new-services/managing-deploy-keys) in the repo so it can `git clone`/`git pull`.

### Deploy

Push to `main`. The workflow runs tests first — if they pass, it deploys. You can watch it under the **Actions** tab.

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
