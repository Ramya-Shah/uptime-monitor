// Package store saves check results in PostgreSQL and computes stats in SQL.
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ramya-Shah/uptime-monitor/internal/checker"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

// Open connects, checks the connection and applies pending migrations.
func Open(ctx context.Context, dbURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool}
	if err := s.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// migrate runs each embedded .sql file once, in name order, recording it in schema_migrations.
func (s *Store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Lock so two monitor replicas starting together don't both migrate.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(4242)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
			return err
		}
		for _, name := range names {
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&done); err != nil {
				return err
			}
			if done {
				continue
			}
			sql, err := migrations.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// Insert saves one check result.
func (s *Store) Insert(ctx context.Context, r checker.Result) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checks (site, checked_at, status, latency_ms, ok, error) VALUES ($1, $2, $3, $4, $5, $6)`,
		r.Site, r.At, r.Status, float64(r.Latency.Microseconds())/1000, r.OK, r.Err)
	return err
}

// Stats summarises one site over a time window.
// Latency percentiles use successful checks only, so timeouts don't distort them.
type Stats struct {
	Site      string   `json:"site"`
	Window    string   `json:"window"`
	Checks    int      `json:"checks"`
	Failures  int      `json:"failures"`
	UptimePct *float64 `json:"uptime_pct"` // nil when there are no checks
	P50Ms     *float64 `json:"p50_ms"`
	P95Ms     *float64 `json:"p95_ms"`
}

func (s *Store) Stats(ctx context.Context, site string, window time.Duration) (Stats, error) {
	st := Stats{Site: site, Window: window.String()}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE NOT ok),
		       100.0 * avg(ok::int),
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE ok),
		       percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) FILTER (WHERE ok)
		FROM checks
		WHERE site = $1 AND checked_at > now() - make_interval(secs => $2)`,
		site, window.Seconds(),
	).Scan(&st.Checks, &st.Failures, &st.UptimePct, &st.P50Ms, &st.P95Ms)
	return st, err
}

// Latest returns the most recent result for every site that has one.
func (s *Store) Latest(ctx context.Context) (map[string]checker.Result, error) {
	return s.latestWhere(ctx, "TRUE")
}

// LastFailures returns the most recent failed check for every site that has one.
func (s *Store) LastFailures(ctx context.Context) (map[string]checker.Result, error) {
	return s.latestWhere(ctx, "NOT ok")
}

// latestWhere picks the newest row per site among rows matching cond.
// cond is always a constant from this file, never user input.
func (s *Store) latestWhere(ctx context.Context, cond string) (map[string]checker.Result, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (site) site, checked_at, status, latency_ms, ok, error
		FROM checks WHERE `+cond+` ORDER BY site, checked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]checker.Result{}
	for rows.Next() {
		var r checker.Result
		var ms float64
		if err := rows.Scan(&r.Site, &r.At, &r.Status, &ms, &r.OK, &r.Err); err != nil {
			return nil, err
		}
		r.Latency = time.Duration(ms * float64(time.Millisecond))
		out[r.Site] = r
	}
	return out, rows.Err()
}
