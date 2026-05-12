package flush

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/Tdude/munin/internal/event"
	"github.com/Tdude/munin/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const redisListScan = "munin:events:*"

type Worker struct {
	redis     *store.Redis
	pg        *store.Postgres
	interval  time.Duration
	batchSize int
}

func New(r *store.Redis, p *store.Postgres, interval time.Duration, batchSize int) *Worker {
	return &Worker{redis: r, pg: p, interval: interval, batchSize: batchSize}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.flushOnce(ctx); err != nil {
				slog.Error("flush: cycle failed", "err", err)
			}
		}
	}
}

func (w *Worker) flushOnce(ctx context.Context) error {
	// SCAN over KEYS to avoid blocking on large keyspaces.
	var cursor uint64
	for {
		keys, next, err := w.redis.Client().Scan(ctx, cursor, redisListScan, 64).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := w.drainList(ctx, key); err != nil {
				slog.Error("flush: drain failed", "key", key, "err", err)
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func (w *Worker) drainList(ctx context.Context, key string) error {
	for {
		// RPopCount needs Redis >= 6.2; pops up to batchSize from the tail (oldest first).
		items, err := w.redis.Client().RPopCount(ctx, key, w.batchSize).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if len(items) == 0 {
			return nil
		}

		events := make([]event.Enriched, 0, len(items))
		for _, raw := range items {
			var e event.Enriched
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				slog.Warn("flush: bad event in queue", "err", err)
				continue
			}
			events = append(events, e)
		}

		if len(events) > 0 {
			if err := w.writeRaw(ctx, events); err != nil {
				slog.Error("flush: write raw failed", "err", err, "n", len(events))
				// Best-effort requeue at tail so order is preserved (oldest goes back to tail).
				for i := len(items) - 1; i >= 0; i-- {
					if pushErr := w.redis.Client().RPush(ctx, key, items[i]).Err(); pushErr != nil {
						slog.Error("flush: requeue failed", "err", pushErr)
						break
					}
				}
				return err
			}
		}

		if len(items) < w.batchSize {
			return nil
		}
	}
}

func (w *Worker) writeRaw(ctx context.Context, events []event.Enriched) error {
	rows := make([][]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, []any{
			e.SiteID, e.VisitorHash, e.SessionHash,
			e.URLPath, nilIfEmpty(e.URLQuery),
			nilIfEmpty(e.ReferrerDomain), nilIfEmpty(e.ReferrerPath),
			nilIfEmpty(e.UABrowser), nilIfEmpty(e.UABrowserVersion),
			nilIfEmpty(e.UAOS), nilIfEmpty(e.UAOSVersion),
			nilIfEmpty(e.UADevice),
			nilIfEmpty(e.Country), nilIfEmpty(e.Language),
			nilIfEmpty(e.Screen), nilIfEmpty(e.Viewport), nilIfEmpty(e.Timezone),
			nilIfFloatZero(e.PixelRatio),
			e.EventName, eventDataValue(e.EventData), e.CreatedAt,
		})
	}

	_, err := w.pg.Pool().CopyFrom(ctx,
		pgx.Identifier{"raw_events"},
		[]string{
			"site_id", "visitor_hash", "session_hash",
			"url_path", "url_query",
			"referrer_domain", "referrer_path",
			"ua_browser", "ua_browser_version",
			"ua_os", "ua_os_version",
			"ua_device",
			"country", "language",
			"screen", "viewport", "timezone",
			"pixel_ratio",
			"event_name", "event_data", "created_at",
		},
		pgx.CopyFromRows(rows),
	)
	return err
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfFloatZero(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func eventDataValue(d map[string]any) any {
	if len(d) == 0 {
		return nil
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil
	}
	return b
}
