package db

import (
	"context"
	"database/sql"
)

type Queries struct {
	db *sql.DB
}

func New(db *sql.DB) *Queries {
	return &Queries{db: db}
}

func (q *Queries) exec(ctx context.Context, query string, args ...interface{}) error {
	_, err := q.db.ExecContext(ctx, query, args...)
	return err
}
