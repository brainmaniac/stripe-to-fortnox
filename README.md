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

- **Revenue account routing by billing country**: Each charge carries a billing country from Stripe's BillingDetails field. The app uses this to pick the correct Fortnox revenue account — configurable in Inställningar → Kontomappning: Sweden (SE or unknown) → 3010, EU member states → 3007, rest of world → 3008. All charges are invoiced with 25% VAT.

- **Charge → Fortnox invoice (B-series)**: For each unsynced succeeded charge, the app creates a customer in Fortnox (if needed) and posts an invoice via `POST /3/invoices`. Fortnox auto-creates the B-series voucher (debit 1510 kundfordringar, credit revenue account + VAT). The Fortnox invoice number is stored on the charge to prevent duplicates.

- **Payout → invoice payment + fee voucher + payout voucher**: When a payout arrives, the app: (1) marks each related invoice as paid via `POST /3/invoicepayments` (Fortnox auto-creates the C-series voucher crediting 1521), (2) creates a fee voucher with reverse VAT (omvänd moms) for each Stripe processing fee, and (3) creates a payout voucher recording the bank transfer: debit 1930, credit 1521.

- **Fee voucher with reverse VAT (omvänd moms)**: For each Stripe processing fee, a voucher is created. Because Stripe Ltd is an Irish EU company, reverse VAT (omvänd skattskyldighet) applies: debit 6065 (payment fee) + debit 2645 (reverse VAT debit), credit 2614 (reverse VAT credit) + credit 1521 (Stripe clearing). The reversal entries cancel each other — only the fee cost hits the P&L.

- **All vouchers are balanced (double-entry)**: Every payout and fee voucher's debit total equals its credit total. The app validates this before calling Fortnox and rejects any imbalanced voucher.

- **Stripe data is idempotent (UPSERT)**: All Stripe data — charges, payouts, customers, balance transactions — is written with `INSERT … ON CONFLICT DO UPDATE`. Re-running a sync or receiving a duplicate webhook event never creates duplicate records.

- **Charge invoices are idempotent**: Once a Fortnox invoice number is stored on a charge (`fortnox_invoice_number`), the charge is excluded from the unsynced list and no second invoice is created. Payout and fee vouchers use the existing two-phase pending/confirmed write pattern.

- **Account 1521 as the Stripe clearing account**: Account 1521 bridges charges and payouts. Every invoice payment credits 1521 (money owed by Stripe), and every payout debits 1521 (money received from Stripe). Its running balance matches the Stripe dashboard balance at all times.

- **Configurable account mappings (Kontomappning)**: Revenue accounts, clearing account, bank account, and fee accounts are configurable in Inställningar → Kontomappning. Defaults match the Swedish BAS-kontoplan.

## Getting Started

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and Docker Compose
- Go 1.23+ (only needed if running outside Docker or running setup wizard)

### What you need from Stripe

1. **Secret API key** — Go to **Stripe Dashboard → Developers → API keys** and copy the **Secret key** (`sk_live_...` or `sk_test_...`). Do not use the publishable key.
2. **Webhook signing secret** — Create a webhook endpoint (see [Stripe Webhook](#stripe-webhook) below), then copy the **Signing secret** (`whsec_...`) shown after creation.

### What you need from Fortnox

1. **Client ID and Client Secret** — You need to create an integration in the Fortnox developer portal:
   - Go to [developer.fortnox.se](https://developer.fortnox.se) and sign in
   - Create a new app and enable the following scopes: **Betalningar**, **Bokföring**, **Faktura**, **Företagsinformation**, **Kostnadsställe**, **Kund**, **Leverantör**, **Leverantörsfaktura**, **Utvecklar-API**
   - Copy the **Client ID** and **Client Secret**
2. **Redirect URI** — Set the redirect URI in your Fortnox app to `https://your-domain/auth/fortnox/callback`. You'll connect the account after the app is running via the settings page.
3. **Fiscal years** — Fortnox requires an open fiscal year (bokföringsår) to exist for any date you want to create vouchers for. Make sure your fiscal years are set up in Fortnox under **Inställningar → Bokföringsår** before syncing historical data.

### 1. Run the setup wizard

```sh
go run ./cmd/setup
```

The wizard walks you through every required key — Stripe, Fortnox, admin password — and writes a `.env` file. The admin password is hashed with bcrypt and the session secret is generated automatically. You only need to run it once.

### 2. Start the app

```sh
docker compose up -d
```

Open http://localhost and log in with the password you set in step 1.

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
newgrp docker                   # apply group change without logging out
```

### One-time server setup

SSH into your VPS and run the setup wizard to create the `.env` file and fix database permissions:

```sh
# Fix database permissions so the container can write to it
sudo chown -R 1000:1000 /opt/stripe-to-fortnox/data
sudo chmod 666 /opt/stripe-to-fortnox/data/app.db
# Also fix the SQLite WAL files if they exist (SQLite write-ahead log — required for writes)
sudo chmod 666 /opt/stripe-to-fortnox/data/app.db-shm /opt/stripe-to-fortnox/data/app.db-wal 2>/dev/null || true
```



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

### Testing Fortnox OAuth and Stripe webhooks locally

Fortnox OAuth and Stripe webhooks require a public HTTPS URL to reach your local server. Use [ngrok](https://ngrok.com) to expose it:

```sh
ngrok http 8080
```

This gives you a URL like `https://abc123.ngrok-free.app`. Then:

1. Set `BASE_URL=https://abc123.ngrok-free.app` in your `.env` and restart the server
2. Update the redirect URI in your Fortnox app to `https://abc123.ngrok-free.app/auth/fortnox/callback`
3. Update your Stripe webhook endpoint to `https://abc123.ngrok-free.app/webhook/stripe`
4. **Access the app via the ngrok URL** (not localhost) — the Fortnox OAuth flow stores state in a session cookie that requires the full HTTPS round-trip to work correctly

Note: the ngrok URL changes every time you restart ngrok (unless you have a paid plan with a fixed domain). You'll need to update the Fortnox redirect URI and Stripe webhook URL each time.

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
