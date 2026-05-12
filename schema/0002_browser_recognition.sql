-- 0002: richer browser / OS / viewport capture.
-- All ADD COLUMN IF NOT EXISTS — safe to re-run.

ALTER TABLE raw_events
    ADD COLUMN IF NOT EXISTS ua_browser_version TEXT,
    ADD COLUMN IF NOT EXISTS ua_os_version      TEXT,
    ADD COLUMN IF NOT EXISTS viewport           TEXT,
    ADD COLUMN IF NOT EXISTS timezone           TEXT,
    ADD COLUMN IF NOT EXISTS pixel_ratio        NUMERIC;
