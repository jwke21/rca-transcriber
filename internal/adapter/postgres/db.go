// Package postgres implements the EngineerRepository, IncidentRepository and
// IncidentEventRepository outbound ports on top of PostgreSQL, using pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pingAttempts is the number of times NewPool pings the database before
// giving up, and pingInterval is the delay between attempts.
const (
	pingAttempts = 5
	pingInterval = time.Second
)

// DB is the subset of pgx methods the repositories in this package use.
// Both *pgxpool.Pool and pgxmock's mock pool satisfy it, which lets the
// repositories be unit tested without a live database.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// rowScanner is satisfied by both pgx.Row (from QueryRow) and pgx.Rows (from
// Query), letting the repositories share one row-scanning helper for both.
type rowScanner interface {
	Scan(dest ...any) error
}

// NewPool creates a pgx connection pool for databaseURL and waits for the
// database to become reachable, pinging up to pingAttempts times, pingInterval
// apart.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}

	var pingErr error
	for attempt := 1; attempt <= pingAttempts; attempt++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			return pool, nil
		}

		if attempt == pingAttempts {
			break
		}

		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("postgres: ping pool: %w", ctx.Err())
		case <-time.After(pingInterval):
		}
	}

	pool.Close()
	return nil, fmt.Errorf("postgres: ping pool after %d attempts: %w", pingAttempts, pingErr)
}
