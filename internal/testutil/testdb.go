// Package testutil provides helpers for setting up test databases.
package testutil

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	"stripe-fortnox-sync/internal/database"
	"stripe-fortnox-sync/internal/db"
)

// NewTestDB opens an in-memory SQLite database with all migrations applied.
// A cleanup hook closes the connection when the test finishes.
func NewTestDB(t *testing.T) *db.Queries {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := database.RunMigrations(sqlDB); err != nil {
		sqlDB.Close()
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return db.New(sqlDB)
}
