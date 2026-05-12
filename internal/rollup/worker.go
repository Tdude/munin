// Package rollup maintains pre-aggregated tables (pageviews_hourly,
// sessions_daily, referrers_daily, countries_daily) from raw_events.
//
// Strategy: each tick re-upserts the *current and previous* time window
// only. Older buckets stop getting recomputed once they fall out of the
// window — they're considered stable. That means we never scan all of
// raw_events; max scan is ~2 hours / 2 days of data.
//
// The dashboard still hits raw_events directly today. The rollup tables
// are scale prep — they'll be wired into the API once event volume makes
// the scans noticeable.
package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Tdude/munin/internal/store"
)

type Worker struct {
	pg       *store.Postgres
	interval time.Duration
}

func New(p *store.Postgres, interval time.Duration) *Worker {
	return &Worker{pg: p, interval: interval}
}

func (w *Worker) Run(ctx context.Context) {
	// Run once at startup so the tables aren't stale after a restart.
	if err := w.runOnce(ctx); err != nil {
		slog.Error("rollup: initial run failed", "err", err)
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.runOnce(ctx); err != nil {
				slog.Error("rollup: cycle failed", "err", err)
			}
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) error {
	start := time.Now()
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"pageviews_hourly", w.pageviewsHourly},
		{"sessions_daily", w.sessionsDailyTotals},
		{"sessions_daily.bounces", w.sessionsDailyBounces},
		{"referrers_daily", w.referrersDaily},
		{"countries_daily", w.countriesDaily},
	}
	for _, s := range steps {
		if err := s.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	slog.Info("rollup: cycle complete", "took", time.Since(start).String())
	return nil
}

// pageviewsHourly re-upserts the current and previous hour buckets.
// Window: [start of (now - 1h), now]. Once a bucket is >1h old it's
// considered stable.
func (w *Worker) pageviewsHourly(ctx context.Context) error {
	const q = `
INSERT INTO pageviews_hourly (site_id, hour, url_path, views, uniques)
SELECT
    site_id,
    date_trunc('hour', created_at) AS hour,
    url_path,
    COUNT(*) FILTER (WHERE event_name = 'pageview') AS views,
    COUNT(DISTINCT visitor_hash) AS uniques
FROM raw_events
WHERE created_at >= date_trunc('hour', NOW() - INTERVAL '1 hour')
  AND url_path IS NOT NULL AND url_path <> ''
GROUP BY site_id, hour, url_path
ON CONFLICT (site_id, hour, url_path) DO UPDATE
SET views = EXCLUDED.views, uniques = EXCLUDED.uniques
`
	_, err := w.pg.Pool().Exec(ctx, q)
	return err
}

// sessionsDailyTotals re-upserts (visitors, visits, pageviews) for today
// and yesterday.
func (w *Worker) sessionsDailyTotals(ctx context.Context) error {
	const q = `
INSERT INTO sessions_daily (site_id, day, visitors, visits, pageviews)
SELECT
    site_id,
    date_trunc('day', created_at)::date AS day,
    COUNT(DISTINCT visitor_hash) AS visitors,
    COUNT(DISTINCT session_hash) AS visits,
    COUNT(*) FILTER (WHERE event_name = 'pageview') AS pageviews
FROM raw_events
WHERE created_at >= date_trunc('day', NOW() - INTERVAL '1 day')
GROUP BY site_id, day
ON CONFLICT (site_id, day) DO UPDATE
SET visitors = EXCLUDED.visitors,
    visits = EXCLUDED.visits,
    pageviews = EXCLUDED.pageviews
`
	_, err := w.pg.Pool().Exec(ctx, q)
	return err
}

// sessionsDailyBounces re-upserts bounce counts (sessions with exactly 1
// pageview) for today and yesterday. Done as a separate query so we can
// keep the totals query simple.
func (w *Worker) sessionsDailyBounces(ctx context.Context) error {
	const q = `
WITH session_pv AS (
    SELECT
        site_id,
        date_trunc('day', created_at)::date AS day,
        session_hash,
        COUNT(*) AS c
    FROM raw_events
    WHERE created_at >= date_trunc('day', NOW() - INTERVAL '1 day')
      AND event_name = 'pageview'
    GROUP BY site_id, day, session_hash
)
INSERT INTO sessions_daily (site_id, day, bounces)
SELECT site_id, day, COUNT(*) FILTER (WHERE c = 1)
FROM session_pv
GROUP BY site_id, day
ON CONFLICT (site_id, day) DO UPDATE
SET bounces = EXCLUDED.bounces
`
	_, err := w.pg.Pool().Exec(ctx, q)
	return err
}

func (w *Worker) referrersDaily(ctx context.Context) error {
	const q = `
INSERT INTO referrers_daily (site_id, day, referrer_domain, views, visitors)
SELECT
    site_id,
    date_trunc('day', created_at)::date AS day,
    referrer_domain,
    COUNT(*) AS views,
    COUNT(DISTINCT visitor_hash) AS visitors
FROM raw_events
WHERE created_at >= date_trunc('day', NOW() - INTERVAL '1 day')
  AND referrer_domain IS NOT NULL AND referrer_domain <> ''
GROUP BY site_id, day, referrer_domain
ON CONFLICT (site_id, day, referrer_domain) DO UPDATE
SET views = EXCLUDED.views, visitors = EXCLUDED.visitors
`
	_, err := w.pg.Pool().Exec(ctx, q)
	return err
}

func (w *Worker) countriesDaily(ctx context.Context) error {
	const q = `
INSERT INTO countries_daily (site_id, day, country, views, visitors)
SELECT
    site_id,
    date_trunc('day', created_at)::date AS day,
    country,
    COUNT(*) AS views,
    COUNT(DISTINCT visitor_hash) AS visitors
FROM raw_events
WHERE created_at >= date_trunc('day', NOW() - INTERVAL '1 day')
  AND country IS NOT NULL AND country <> ''
GROUP BY site_id, day, country
ON CONFLICT (site_id, day, country) DO UPDATE
SET views = EXCLUDED.views, visitors = EXCLUDED.visitors
`
	_, err := w.pg.Pool().Exec(ctx, q)
	return err
}
