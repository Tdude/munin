# Deploy Muntra to Cloud Run

One-shot deployment of Muntra as a Cloud Run service in a GCP project of your
choice, region `europe-north1` (Stockholm). Provisions everything Muntra needs:
Cloud SQL Postgres + Memorystore Redis + Serverless VPC Connector + Secret
Manager entries + Cloud Run service.

## Pick a project model first

**Recommended: your own GCP project (`muntra-prod`).** Muntra is your service;
Heymy is one of many possible consumers (idunworks, obojen, etc. can point at
the same instance). Cross-project boundary = clean HTTPS API, no shared IAM.
Cost: ~$53-63/month.

**Alternative: Heymy's `heymy-dev` project.** Cheaper by ~$8/month because the
existing `heymy-db` Cloud SQL instance is reused. Requires that Wilmer grant
your identity Owner/Editor on `heymy-dev`:

```bash
# Wilmer runs once, on heymy-dev:
gcloud projects add-iam-policy-binding heymy-dev \
  --member="user:YOUR_EMAIL@gmail.com" \
  --role="roles/editor"
```

Billing then lands on Wilmer's billing account. To use this mode, pass:

```bash
PROJECT_ID=heymy-dev CLOUDSQL_INSTANCE=heymy-db \
  bash deploy/heymy-dev/bootstrap.sh
```

The script auto-detects whether the named instance exists and reuses it if so,
otherwise creates a fresh one.

## Cost — read before running

Recurring monthly cost in `europe-north1`:

| Resource | Tier | Cost |
|---|---|---|
| Memorystore Redis | Basic, 1 GB | ~$35 |
| Serverless VPC Access Connector | 2× e2-micro instances | ~$8–12 (idle) |
| Cloud SQL `muntra-db` instance | db-f1-micro (own project) | ~$7-8 |
| Cloud SQL `muntra` database | (shared instance — heymy-dev mode) | ~$0 |
| Cloud Run service | scale-to-zero, low traffic | ~$0–3 |
| Artifact Registry, Secret Manager | small | ~$0 |
| **Total — own project** | | **~$53–63 / month (≈ 555–660 SEK)** |
| **Total — heymy-dev shared** | | **~$45–55 / month (≈ 470–580 SEK)** |

If that's too much, the cheaper alternative is to host Redis on Upstash
(~$10/mo, EU regions, but adds a US-incorporated sub-processor to your DPA
— see "Cost reduction" below). With Upstash + own project: ~$28-33/mo.

## Prereqs (on your workstation)

```bash
brew install --cask google-cloud-sdk
gcloud auth login
gcloud auth application-default login
```

You must be Project Owner / Editor on the target project.

### Creating your own project (recommended path)

```bash
# Pick a unique project ID; "muntra-prod" or "tdude-muntra" both work.
PROJECT_ID=muntra-prod

# Find your billing account ID:
gcloud billing accounts list

# Create the project and link billing:
gcloud projects create "$PROJECT_ID" --name="Muntra"
gcloud billing projects link "$PROJECT_ID" --billing-account=BILLING_ACCOUNT_ID

# (Optional but recommended) Add a budget alert mirroring Heymy's 1000 SEK guard:
gcloud billing budgets create \
  --billing-account=BILLING_ACCOUNT_ID \
  --display-name="Muntra monthly cap" \
  --budget-amount=100 \
  --threshold-rule=percent=0.5 \
  --threshold-rule=percent=0.9 \
  --threshold-rule=percent=1.0
```

## Deploy

From the repo root:

```bash
bash deploy/heymy-dev/bootstrap.sh
```

The script:
1. Enables required APIs (`run`, `sqladmin`, `redis`, `vpcaccess`,
   `secretmanager`, `artifactregistry`, `cloudbuild`).
2. Generates `MUNTRA_DB_PASSWORD` + `MUNTRA_DASHBOARD_TOKEN` (skipped on
   re-run — existing values reused).
3. Creates the `muntra` database + role on the existing `heymy-db` Cloud SQL
   instance.
4. Composes `MUNTRA_POSTGRES_DSN` using the Cloud SQL unix-socket DSN format
   so Cloud Run can reach the database with no VPC routing.
5. Provisions Serverless VPC Access Connector `muntra-connector`
   (range `10.8.16.0/28` in the `default` VPC).
6. Provisions Memorystore Redis Basic 1 GB (`muntra-redis`) via DIRECT_PEERING
   on the same VPC.
7. Builds the Muntra container via Cloud Build (no local Docker daemon
   required) and pushes to Artifact Registry.
8. Grants the Cloud Run runtime service account
   (`PROJECT_NUMBER-compute@developer.gserviceaccount.com`) `secretAccessor`
   on the three secrets the service consumes.
9. Deploys the `muntra` Cloud Run service with:
   - `--allow-unauthenticated` (the `/collect` and `/script.js` endpoints
     are designed to be reached from any browser; `/api/*` is bearer-protected)
   - `--add-cloudsql-instances` for Postgres
   - `--vpc-connector` for Redis
   - All env vars + secret mounts wired
10. `curl /health` smoke test.
11. Prints the snippet you run next to wire Heymy.

Schema migrations apply automatically on container startup (Muntra
re-runs `schema/*.sql` on every boot; all migrations are idempotent).

## Re-runs

The script is idempotent: re-running it skips already-provisioned resources,
keeps existing secrets, and updates the Cloud SQL role password from Secret
Manager (in case it drifts). Use this for picking up code changes — the build
step pushes a fresh image and `gcloud run deploy` creates a new revision.

## Wire Heymy

After the script finishes, it prints two commands:

```bash
gcloud secrets create MUNTRA_DASHBOARD_TOKEN ...   # add the token to Heymy's secrets
gcloud run services update heymy-app ...           # set env + secret refs
```

These can also be re-run safely. The Heymy-side PR (`feat/muntra-integration`)
already reads these variables — the moment you redeploy Heymy after the env
update, the tracker fires and `/dashboard/analytics` shows real data.

## Verify

```bash
URL=$(gcloud run services describe muntra --region=europe-north1 --project=heymy-dev --format='value(status.url)')
TOKEN=$(gcloud secrets versions access latest --secret=MUNTRA_DASHBOARD_TOKEN --project=heymy-dev)

curl "$URL/health"
# {"status":"ok"}

curl -H "Authorization: Bearer $TOKEN" "$URL/api/stats?site=heymy"
# {"pageviews":{"value":0,"prev":0},"visitors":{"value":0,"prev":0},"visits":{"value":0,"prev":0}}

# Tail logs
gcloud run services logs read muntra --region=europe-north1 --project=heymy-dev --limit=50
```

## Hardening (recommended once stable)

1. **Tighten `MUNTRA_SITE_ORIGINS`** — anti-spoof per-site Origin allowlist.
   ```bash
   gcloud run services update muntra --region=europe-north1 \
     --update-env-vars='MUNTRA_SITE_ORIGINS=heymy:heymy.app,heymy-app-982668910493.europe-north1.run.app'
   ```
2. **First-party serve** under `heymy.app/m/` so ad-blockers don't recognize
   the script-src host. Add a Next.js rewrite in `next.config.ts` of Heymy:
   ```ts
   rewrites: async () => [
     { source: "/m/:path*", destination: `${process.env.MUNTRA_BASE_URL}/:path*` },
   ],
   ```
   Then change Heymy's `MUNTRA_BASE_URL` to `https://heymy.app/m`. Muntra's
   tracker auto-derives `/m/collect` from its own `src` URL.
3. **Move `--allow-unauthenticated` off `/api/*`** — split the service into two
   Cloud Run services (`muntra-collect` public, `muntra-api` IAM-protected),
   or front it with Cloud Armor / API Gateway. Bearer token auth on `/api/*`
   already provides defense-in-depth; this is belt-and-braces.

## Cost reduction (if needed)

To cut ~$25/month, swap Memorystore for Upstash Redis (EU region, RESP-over-
TLS):

```bash
# 1) Sign up at https://upstash.com, create a Redis DB in `eu-central-1` or
#    `eu-west-1`. Copy the rediss:// URL (note the TWO 's's — TLS).
# 2) Replace the MUNTRA_REDIS_URL secret:
echo -n "$UPSTASH_URL" | gcloud secrets versions add MUNTRA_REDIS_URL \
  --project=heymy-dev --data-file=-
# 3) Remove the VPC connector dependency from the Cloud Run service:
gcloud run services update muntra --region=europe-north1 \
  --clear-vpc-connector --vpc-egress=all-traffic
# 4) Delete the connector to stop billing:
gcloud compute networks vpc-access connectors delete muntra-connector \
  --region=europe-north1 --project=heymy-dev
```

Trade-off: Upstash, Inc. (US-incorporated, data in EU) becomes a sub-processor.
Update Heymy's DPA / ROPA accordingly. The legal posture is still fine for
GDPR (EU/EEA data location, no third-country transfer), but it's a more
involved compliance story than the all-`heymy-dev` default.

## Teardown

```bash
bash deploy/heymy-dev/teardown.sh                # keeps secrets
bash deploy/heymy-dev/teardown.sh --purge-secrets
```

Stops all recurring billing within a few minutes (Memorystore stop is the big
one; Cloud Run scale-to-zero stops immediately).
