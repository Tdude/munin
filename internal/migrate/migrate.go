// Package migrate applies any *.sql files in a directory to Postgres in
// lexical order on startup. Every migration must be idempotent (CREATE
// TABLE IF NOT EXISTS, ADD COLUMN IF NOT EXISTS, etc.) — no version
// tracking table; we just re-run everything every boot. Cheap for small
// schemas and removes "did the migration apply?" footguns.
package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tdude/munin/internal/store"
)

func Apply(ctx context.Context, pg *store.Postgres, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		path := filepath.Join(dir, f)
		sql, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		start := time.Now()
		if _, err := pg.Pool().Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
		slog.Info("migrate: applied", "file", f, "took", time.Since(start).String())
	}
	return nil
}
