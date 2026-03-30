// setup is an interactive terminal wizard that generates a .env file for
// stripe-fortnox-sync. Run it once before the first launch.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// ── ANSI helpers ──────────────────────────────────────────────────────────────

const (
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	red    = "\033[31m"
	reset  = "\033[0m"
)

func header(s string) { fmt.Printf("\n%s%s%s\n", bold+cyan, s, reset) }
func note(s string)   { fmt.Printf("%s%s%s\n", dim, s, reset) }
func ok(s string)     { fmt.Printf("%s✓ %s%s\n", green, s, reset) }
func warn(s string)   { fmt.Printf("%s⚠ %s%s\n", yellow, s, reset) }

// ── Input helpers ─────────────────────────────────────────────────────────────

var reader = bufio.NewReader(os.Stdin)

// prompt shows a prompt and reads a line. Uses defaultVal if the user presses enter.
func prompt(label, defaultVal, hint string) string {
	if hint != "" {
		note(hint)
	}
	if defaultVal != "" {
		fmt.Printf("%s%s%s [%s%s%s]: ", bold, label, reset, dim, defaultVal, reset)
	} else {
		fmt.Printf("%s%s%s: ", bold, label, reset)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// promptRequired keeps asking until the user enters something.
func promptRequired(label, hint string) string {
	for {
		v := prompt(label, "", hint)
		if v != "" {
			return v
		}
		warn("This field is required.")
	}
}

// promptPassword reads a password without echoing it to the terminal.
func promptPassword(label, hint string) string {
	if hint != "" {
		note(hint)
	}
	fmt.Printf("%s%s%s: ", bold, label, reset)
	var raw []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
	} else {
		// Non-interactive (pipe / test) — fall back to plain read.
		line, _ := reader.ReadString('\n')
		raw = []byte(strings.TrimSpace(line))
	}
	if err != nil || len(raw) == 0 {
		warn("No password entered.")
		return ""
	}
	return string(raw)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	fmt.Printf("\n%s%s stripe-fortnox-sync — setup%s\n", bold, cyan, reset)
	fmt.Println(strings.Repeat("─", 42))
	note("This wizard creates a .env file with all required configuration.")
	note("You can re-run it at any time to update values.")

	// ── Check for existing .env ───────────────────────────────────────────────
	if _, err := os.Stat(".env"); err == nil {
		warn(".env already exists. Continuing will overwrite it.")
		confirm := prompt("Continue?", "n", "")
		if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
			fmt.Println("Aborted.")
			os.Exit(0)
		}
	}

	cfg := map[string]string{}

	// ── Admin password ────────────────────────────────────────────────────────
	header("1 / 6 — Admin password")
	note("This is the password you'll use to log in to the web UI.")
	var hash string
	for {
		pw := promptPassword("Password", "")
		if pw == "" {
			warn("Password cannot be empty.")
			continue
		}
		pw2 := promptPassword("Confirm password", "")
		if pw != pw2 {
			warn("Passwords do not match. Try again.")
			continue
		}
		b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%serror hashing password: %v%s\n", red, err, reset)
			os.Exit(1)
		}
		hash = string(b)
		break
	}
	cfg["ADMIN_PASSWORD_HASH"] = hash
	ok("Password hashed with bcrypt.")

	// ── Session secret ────────────────────────────────────────────────────────
	header("2 / 6 — Session secret")
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		fmt.Fprintf(os.Stderr, "%serror generating secret: %v%s\n", red, err, reset)
		os.Exit(1)
	}
	cfg["SESSION_SECRET"] = hex.EncodeToString(secret)
	ok("Session secret generated automatically.")

	// ── Stripe ────────────────────────────────────────────────────────────────
	header("3 / 6 — Stripe")
	note("Find these in the Stripe Dashboard → Developers → API keys / Webhooks.")
	cfg["STRIPE_API_KEY"] = promptRequired("Stripe API key (sk_live_… or sk_test_…)", "")
	cfg["STRIPE_WEBHOOK_SECRET"] = prompt(
		"Stripe webhook secret (whsec_…)",
		"",
		"Leave blank for now — you can add it after setting up the webhook endpoint.",
	)

	// ── Fortnox ───────────────────────────────────────────────────────────────
	header("4 / 6 — Fortnox")
	note("Create an integration at developer.fortnox.se → My integrations.")
	note("Set the redirect URI to: <BASE_URL>/settings/fortnox/callback")
	cfg["FORTNOX_CLIENT_ID"] = promptRequired("Fortnox client ID", "")
	cfg["FORTNOX_CLIENT_SECRET"] = promptRequired("Fortnox client secret", "")

	// ── App URL ───────────────────────────────────────────────────────────────
	header("5 / 6 — App URL")
	cfg["BASE_URL"] = prompt(
		"Base URL",
		"http://localhost:8080",
		"The public URL where this app is reachable. Used for the Fortnox OAuth callback.",
	)

	// ── Port & database ───────────────────────────────────────────────────────
	header("6 / 6 — Server (optional)")
	cfg["APP_PORT"] = prompt("HTTP port", "8080", "")
	cfg["DB_PATH"] = prompt("Database file path", "./data/app.db", "")

	// ── Write .env ────────────────────────────────────────────────────────────
	if err := writeEnv(".env", cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%serror writing .env: %v%s\n", red, err, reset)
		os.Exit(1)
	}

	fmt.Printf("\n%s%s.env written successfully.%s\n", bold+green, "✓ ", reset)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  %s1.%s Start the app:          %sgo run ./cmd/server%s\n", bold, reset, cyan, reset)
	fmt.Printf("  %s2.%s Open the web UI:         %s%s%s\n", bold, reset, cyan, cfg["BASE_URL"], reset)
	fmt.Printf("  %s3.%s Connect Fortnox:         Inställningar → Anslut Fortnox\n", bold, reset)
	if cfg["STRIPE_WEBHOOK_SECRET"] == "" {
		fmt.Printf("  %s4.%s Add webhook secret:     re-run setup or edit .env after creating the\n", bold, reset)
		fmt.Printf("         Stripe webhook pointing to %s/webhook/stripe\n", cfg["BASE_URL"])
	}
	fmt.Println()
}

// writeEnv writes key=value pairs to path in a stable order.
func writeEnv(path string, cfg map[string]string) error {
	order := []string{
		"ADMIN_PASSWORD_HASH",
		"SESSION_SECRET",
		"STRIPE_API_KEY",
		"STRIPE_WEBHOOK_SECRET",
		"FORTNOX_CLIENT_ID",
		"FORTNOX_CLIENT_SECRET",
		"BASE_URL",
		"APP_PORT",
		"DB_PATH",
	}

	var sb strings.Builder
	sb.WriteString("# Generated by `go run ./cmd/setup` — edit as needed.\n\n")
	for _, k := range order {
		v, ok := cfg[k]
		if !ok {
			continue
		}
		// Quote values that contain spaces or special characters.
		if strings.ContainsAny(v, " \t#$'\"\\") {
			v = "'" + strings.ReplaceAll(v, "'", "'\\''") + "'"
		}
		sb.WriteString(k + "=" + v + "\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0600)
}
