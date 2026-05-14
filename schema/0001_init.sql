-- Muntra analytics schema, version 1.
-- Apply manually for now: psql "$MUNTRA_POSTGRES_DSN" -f schema/0001_init.sql

CREATE TABLE IF NOT EXISTS raw_events (
    id                  BIGSERIAL PRIMARY KEY,
    site_id             TEXT        NOT NULL,
    visitor_hash        TEXT        NOT NULL,
    session_hash        TEXT        NOT NULL,
    url_path            TEXT        NOT NULL,
    url_query           TEXT,
    referrer_domain     TEXT,
    referrer_path       TEXT,
    ua_browser          TEXT,
    ua_browser_version  TEXT,
    ua_os               TEXT,
    ua_os_version       TEXT,
    ua_device           TEXT,
    country             TEXT,
    language            TEXT,
    screen              TEXT,
    viewport            TEXT,
    timezone            TEXT,
    pixel_ratio         NUMERIC,
    event_name          TEXT        NOT NULL,
    event_data          JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_raw_events_site_time ON raw_events (site_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_raw_events_visitor   ON raw_events (visitor_hash, created_at DESC);

-- Pre-aggregated tables. Populated by a rollup job (not yet implemented; see flush worker TODO).
CREATE TABLE IF NOT EXISTS pageviews_hourly (
    site_id   TEXT        NOT NULL,
    hour      TIMESTAMPTZ NOT NULL,
    url_path  TEXT        NOT NULL,
    views     INTEGER     NOT NULL DEFAULT 0,
    uniques   INTEGER     NOT NULL DEFAULT 0,
    PRIMARY KEY (site_id, hour, url_path)
);
CREATE INDEX IF NOT EXISTS idx_pageviews_hourly_site_hour ON pageviews_hourly (site_id, hour DESC);

CREATE TABLE IF NOT EXISTS sessions_daily (
    site_id      TEXT    NOT NULL,
    day          DATE    NOT NULL,
    visitors     INTEGER NOT NULL DEFAULT 0,
    visits       INTEGER NOT NULL DEFAULT 0,
    pageviews    INTEGER NOT NULL DEFAULT 0,
    bounces      INTEGER NOT NULL DEFAULT 0,
    avg_duration INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (site_id, day)
);

CREATE TABLE IF NOT EXISTS referrers_daily (
    site_id         TEXT    NOT NULL,
    day             DATE    NOT NULL,
    referrer_domain TEXT    NOT NULL,
    views           INTEGER NOT NULL DEFAULT 0,
    visitors        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (site_id, day, referrer_domain)
);

CREATE TABLE IF NOT EXISTS countries_daily (
    site_id  TEXT    NOT NULL,
    day      DATE    NOT NULL,
    country  TEXT    NOT NULL,
    views    INTEGER NOT NULL DEFAULT 0,
    visitors INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (site_id, day, country)
);
