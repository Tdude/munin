package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr       string
	RedisURL       string
	PostgresDSN    string
	AllowedSites   map[string]bool
	// SiteOrigins maps site_id → set of allowed Origin/Referer hosts.
	// When set for a site, /collect rejects events from other hosts. When
	// empty for a site (or unset entirely), origin validation is skipped
	// for that site — backward-compatible default.
	SiteOrigins    map[string]map[string]bool
	FlushInterval  time.Duration
	FlushBatchSize int
	RollupInterval time.Duration
	DashboardToken string
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:       getEnv("MUNIN_HTTP_ADDR", ":8090"),
		RedisURL:       getEnv("MUNIN_REDIS_URL", "redis://localhost:6379/0"),
		PostgresDSN:    os.Getenv("MUNIN_POSTGRES_DSN"),
		AllowedSites:   parseList(getEnv("MUNIN_ALLOWED_SITES", "")),
		FlushInterval:  parseDuration(getEnv("MUNIN_FLUSH_INTERVAL", "60s"), 60*time.Second),
		FlushBatchSize: parseInt(getEnv("MUNIN_FLUSH_BATCH_SIZE", "500"), 500),
		RollupInterval: parseDuration(getEnv("MUNIN_ROLLUP_INTERVAL", "15m"), 15*time.Minute),
		DashboardToken: os.Getenv("MUNIN_DASHBOARD_TOKEN"),
		SiteOrigins:    parseSiteOrigins(os.Getenv("MUNIN_SITE_ORIGINS")),
	}
	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("MUNIN_POSTGRES_DSN required")
	}
	if len(cfg.AllowedSites) == 0 {
		return nil, fmt.Errorf("MUNIN_ALLOWED_SITES required (comma-separated site ids)")
	}
	if cfg.DashboardToken == "" {
		return nil, fmt.Errorf("MUNIN_DASHBOARD_TOKEN required (shared secret for /api/* endpoints)")
	}
	return cfg, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseList(s string) map[string]bool {
	m := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			m[part] = true
		}
	}
	return m
}

func parseDuration(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func parseInt(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// parseSiteOrigins reads MUNIN_SITE_ORIGINS in the form:
//   site_id:host1,host2|site_id:host3|...
// e.g. "obojen:obojen.com,www.obojen.com|rebelkayaks:rebelkayaks.eu"
func parseSiteOrigins(s string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if s == "" {
		return out
	}
	for _, pair := range strings.Split(s, "|") {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		siteID := strings.TrimSpace(parts[0])
		hosts := map[string]bool{}
		for _, h := range strings.Split(parts[1], ",") {
			h = strings.TrimSpace(strings.ToLower(h))
			if h != "" {
				hosts[h] = true
			}
		}
		if siteID != "" && len(hosts) > 0 {
			out[siteID] = hosts
		}
	}
	return out
}
