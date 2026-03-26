package config

import (
	"fmt"
	"os"
)

type Config struct {
	AppPort             string
	DBPath              string
	AdminPasswordHash   string
	SessionSecret       string
	StripeAPIKey        string
	StripeWebhookSecret string
	FortnoxClientID     string
	FortnoxClientSecret string
	BaseURL             string
}

func Load() (*Config, error) {
	cfg := &Config{
		AppPort:             getEnv("APP_PORT", "8080"),
		DBPath:              getEnv("DB_PATH", "./data/app.db"),
		AdminPasswordHash:   os.Getenv("ADMIN_PASSWORD_HASH"),
		SessionSecret:       os.Getenv("SESSION_SECRET"),
		StripeAPIKey:        os.Getenv("STRIPE_API_KEY"),
		StripeWebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
		FortnoxClientID:     os.Getenv("FORTNOX_CLIENT_ID"),
		FortnoxClientSecret: os.Getenv("FORTNOX_CLIENT_SECRET"),
		BaseURL:             getEnv("BASE_URL", "http://localhost:8080"),
	}

	if cfg.AdminPasswordHash == "" {
		return nil, fmt.Errorf("ADMIN_PASSWORD_HASH is required")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("SESSION_SECRET is required")
	}
	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
