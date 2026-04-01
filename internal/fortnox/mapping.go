package fortnox

import (
	"context"
	"fmt"

	"stripe-fortnox-sync/internal/db"
)

// Kontotyp constants used in account_mappings.
const (
	KontotypIntäktskonto     = "Intäktskonto"
	KontotypAvstämningskonto = "Avstämningskonto"
	KontotypUtbetalningskonto = "Utbetalningskonto"
	KontotypBetalväxelAvgift = "BetalväxelAvgift"
	KontotypOmvändMomsDebet  = "OmvändMomsDebet"
	KontotypOmvändMomsKredit = "OmvändMomsKredit"

	// Account 1510 (Kundfordringar) is the receivable account created by B-series invoices.
	// It is hardcoded since it comes from Fortnox's invoicing system.
	AccountKundfordringar = "1510"
)

// MappingResolver resolves account numbers from the account_mappings table.
type MappingResolver struct {
	queries *db.Queries
}

func NewMappingResolver(queries *db.Queries) *MappingResolver {
	return &MappingResolver{queries: queries}
}

// countryGroup returns the matchkod ("SE", "EU", or "WO") for a billing country code.
func countryGroup(countryCode string) string {
	if countryCode == "" || countryCode == "SE" {
		return "SE"
	}
	if euCountries[countryCode] {
		return "EU"
	}
	return "WO"
}

// RevenueMapping returns the account mapping for a billing country code.
func (r *MappingResolver) RevenueMapping(ctx context.Context, countryCode string) (*db.AccountMapping, error) {
	group := countryGroup(countryCode)
	m, err := r.queries.GetAccountMapping(ctx, KontotypIntäktskonto, group)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("no account mapping for %s/%s", KontotypIntäktskonto, group)
	}
	return m, nil
}

// AccountByKontotyp returns the account number for a kontotyp and matchkod.
func (r *MappingResolver) AccountByKontotyp(ctx context.Context, kontotyp, matchkod string) (string, error) {
	m, err := r.queries.GetAccountMapping(ctx, kontotyp, matchkod)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", fmt.Errorf("no account mapping for %s/%s", kontotyp, matchkod)
	}
	return m.Konto, nil
}
