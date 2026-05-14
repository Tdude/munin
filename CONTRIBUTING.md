# Contributing to Muntra

PRs and issues are welcome. Muntra is intentionally small — the goal is to stay readable and roughly fit in your head.

## What I'd love help with

- **MaxMind GeoLite2 wiring** so the `country` column actually populates.
- **Reference dashboard** — a tiny single-page UI consuming `/api/*` (kept separate from this repo since the ingest layer shouldn't grow).
- **Bot heuristics beyond UA** — request-shape signals that catch headless browsers.
- **Tests** — there are very few right now. Especially around the rollup worker and the daily-salt rotation race.

## How to run locally

```sh
cp .env.example .env
$EDITOR .env                              # set passwords + sites
docker compose up -d
docker logs -f muntra-muntra-1               # follow the binary
```

The Go binary auto-applies migrations on startup, so `schema/*.sql` changes pick up on next restart.

For a fast Go-only dev loop (no Docker rebuild on every edit), point the binary at a local Postgres + Redis:

```sh
MUNTRA_POSTGRES_DSN='postgres://muntra:dev@127.0.0.1:5432/muntra?sslmode=disable' \
MUNTRA_REDIS_URL='redis://127.0.0.1:6379/0' \
MUNTRA_ALLOWED_SITES='dev' \
MUNTRA_DASHBOARD_TOKEN='devtoken' \
go run .
```

## Code style

- **Go**: gofmt + go vet clean before commit. Idiomatic patterns — small interfaces, accept-interfaces-return-structs, errors wrapped with `%w`.
- **Schema**: every migration is **idempotent** — `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`. The runner re-applies all files every boot. New files only — never edit a committed migration.
- **No external network calls at request time** — geo, ASN, etc. must use embedded data (MaxMind .mmdb mounted at runtime). The whole point is bounded latency and no third-party dependencies on the hot path.

## License (AGPL-3.0-or-later)

Contributions are accepted under the same license. If you self-host a modified version, AGPL requires you to publish the modifications under AGPL too.
