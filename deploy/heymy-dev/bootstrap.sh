#!/usr/bin/env bash
# Bootstrap Muntra as a Cloud Run service inside the `heymy-dev` GCP project.
#
# This script is idempotent — safe to re-run. It will:
#   1. Verify gcloud auth + APIs enabled
#   2. Generate MUNTRA_DB_PASSWORD + MUNTRA_DASHBOARD_TOKEN (skip if exist)
#   3. Create the `muntra` database + role on the existing Cloud SQL instance
#   4. Provision a Serverless VPC Access Connector (for Memorystore reach)
#   5. Provision a Memorystore Redis Basic 1GB instance
#   6. Build + push the Muntra container via Cloud Build
#   7. Deploy the Cloud Run service with all secrets + env wired
#   8. Print the Heymy-side env-var update snippet
#
# Cost estimate (europe-north1, monthly):
#   - Memorystore Basic 1GB:         ~$35
#   - Serverless VPC Connector:      ~$8-12 idle
#   - Cloud Run (scale-to-zero):     ~$0-3 at low traffic
#   - Cloud SQL marginal:            ~$0 (same instance, separate DB)
#   - Artifact Registry, Secret Mgr: ~$0
#   ─────────────────────────────────────────
#   Total:                           ~$45-55 / month (≈ 470-580 SEK)
#
# Prereqs:
#   - gcloud CLI installed and authenticated as a Project Owner / Editor on
#     heymy-dev
#   - You have the Cloud SQL instance name handy (default: heymy-db)
#
# Usage:
#   bash deploy/heymy-dev/bootstrap.sh

set -euo pipefail

# ────────────────────────────────────────────────────────────────
# Configuration — override via env if needed
# ────────────────────────────────────────────────────────────────
PROJECT_ID="${PROJECT_ID:-muntra-prod}"
REGION="${REGION:-europe-north1}"
SERVICE_NAME="${SERVICE_NAME:-muntra}"
IMAGE_REPO="${IMAGE_REPO:-muntra}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT_ID}/${IMAGE_REPO}/${SERVICE_NAME}:${IMAGE_TAG}"

# Cloud SQL: the script will reuse an existing instance with this name or
# create one if it doesn't exist (db-f1-micro by default, ~\$7-8/mo).
# To reuse Heymy's instance instead: PROJECT_ID=heymy-dev CLOUDSQL_INSTANCE=heymy-db.
CLOUDSQL_INSTANCE="${CLOUDSQL_INSTANCE:-muntra-db}"
CLOUDSQL_TIER="${CLOUDSQL_TIER:-db-f1-micro}"
CLOUDSQL_VERSION="${CLOUDSQL_VERSION:-POSTGRES_16}"
DB_NAME="${DB_NAME:-muntra}"
DB_ROLE="${DB_ROLE:-muntra}"

REDIS_NAME="${REDIS_NAME:-muntra-redis}"
REDIS_SIZE_GB="${REDIS_SIZE_GB:-1}"
REDIS_VERSION="${REDIS_VERSION:-redis_7_2}"

VPC_NETWORK="${VPC_NETWORK:-default}"
CONNECTOR_NAME="${CONNECTOR_NAME:-muntra-connector}"
CONNECTOR_RANGE="${CONNECTOR_RANGE:-10.8.16.0/28}"

# Site IDs Heymy will report under. Comma-separated, no spaces.
ALLOWED_SITES="${ALLOWED_SITES:-heymy,heymy-dev}"
# Per-site Origin allowlist for anti-spoof. `site:host1,host2|site:host3`.
# Leave empty if you want to defer this hardening.
SITE_ORIGINS="${SITE_ORIGINS:-}"

# Cloud Run sizing — keep modest by default.
MIN_INSTANCES="${MIN_INSTANCES:-0}"
MAX_INSTANCES="${MAX_INSTANCES:-3}"
CPU="${CPU:-1}"
MEMORY="${MEMORY:-256Mi}"

# ────────────────────────────────────────────────────────────────
# Helpers
# ────────────────────────────────────────────────────────────────
ok()   { printf "  \033[32m✓\033[0m %s\n" "$*"; }
info() { printf "  \033[36m→\033[0m %s\n" "$*"; }
warn() { printf "  \033[33m!\033[0m %s\n" "$*"; }
step() { printf "\n\033[1m%s\033[0m\n" "$*"; }

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

confirm() {
  local prompt="$1"
  read -r -p "$prompt [y/N] " ans
  case "$ans" in
    y|Y|yes) return 0 ;;
    *) return 1 ;;
  esac
}

secret_exists() {
  gcloud secrets describe "$1" --project="$PROJECT_ID" >/dev/null 2>&1
}

ensure_secret() {
  local name="$1"
  local value="$2"
  if secret_exists "$name"; then
    ok "secret $name already exists (kept)"
  else
    printf '%s' "$value" | gcloud secrets create "$name" \
      --project="$PROJECT_ID" \
      --replication-policy=user-managed \
      --locations="$REGION" \
      --data-file=- >/dev/null
    ok "secret $name created"
  fi
}

upsert_secret() {
  # Always adds a new version. Use when value changes legitimately.
  local name="$1"
  local value="$2"
  if ! secret_exists "$name"; then
    printf '%s' "$value" | gcloud secrets create "$name" \
      --project="$PROJECT_ID" \
      --replication-policy=user-managed \
      --locations="$REGION" \
      --data-file=- >/dev/null
    ok "secret $name created"
  else
    printf '%s' "$value" | gcloud secrets versions add "$name" \
      --project="$PROJECT_ID" \
      --data-file=- >/dev/null
    ok "secret $name updated (new version)"
  fi
}

# ────────────────────────────────────────────────────────────────
# Preflight
# ────────────────────────────────────────────────────────────────
step "Preflight"
require gcloud
require openssl

gcloud config set project "$PROJECT_ID" >/dev/null
ACTIVE=$(gcloud config get-value account 2>/dev/null)
ok "gcloud authed as $ACTIVE on project $PROJECT_ID"

# Detect whether Cloud SQL needs creating so the cost preview is accurate.
SQL_INSTANCE_EXISTS=false
if gcloud sql instances describe "$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
  SQL_INSTANCE_EXISTS=true
fi

step "About to provision Muntra in $PROJECT_ID / $REGION"
cat <<EOF
  Service:           Cloud Run        $SERVICE_NAME
  Image:             Artifact Reg.    $IMAGE
  Database:          Cloud SQL        $CLOUDSQL_INSTANCE $($SQL_INSTANCE_EXISTS && echo "(reusing)" || echo "(WILL BE CREATED, $CLOUDSQL_TIER)")
                                      db: $DB_NAME, role: $DB_ROLE
  Cache:             Memorystore      $REDIS_NAME (Basic, ${REDIS_SIZE_GB}GB)
  Network:           VPC Connector    $CONNECTOR_NAME ($CONNECTOR_RANGE in $VPC_NETWORK)
  Site IDs accepted: $ALLOWED_SITES
  Origin allowlist:  ${SITE_ORIGINS:-<none — set SITE_ORIGINS env to harden>}

  Estimated recurring cost:
    - Memorystore Basic 1GB:      ~\$35
    - VPC Connector (idle):       ~\$8-12
    - Cloud Run (scale-to-zero):  ~\$0-3
$($SQL_INSTANCE_EXISTS \
    && echo "    - Cloud SQL (shared):         ~\$0 (instance reused)" \
    || echo "    - Cloud SQL $CLOUDSQL_TIER:        ~\$7-8 (new instance)")
    ─────────────────────────────────────
$($SQL_INSTANCE_EXISTS \
    && echo "    Total:                        ~\$45-55/month (≈ 470-580 SEK)" \
    || echo "    Total:                        ~\$53-63/month (≈ 555-660 SEK)")
EOF
echo
confirm "Proceed?" || { echo "Aborted."; exit 1; }

# ────────────────────────────────────────────────────────────────
# Enable APIs
# ────────────────────────────────────────────────────────────────
step "Enabling required APIs"
gcloud services enable \
  cloudbuild.googleapis.com \
  run.googleapis.com \
  sqladmin.googleapis.com \
  redis.googleapis.com \
  vpcaccess.googleapis.com \
  secretmanager.googleapis.com \
  artifactregistry.googleapis.com \
  servicenetworking.googleapis.com \
  --project="$PROJECT_ID" >/dev/null
ok "APIs enabled"

# ────────────────────────────────────────────────────────────────
# Secrets
# ────────────────────────────────────────────────────────────────
step "Generating secrets (if not already present)"
DB_PASSWORD=$(openssl rand -base64 32 | tr -d '\n' | tr '/+=' '_-A')
DASHBOARD_TOKEN=$(openssl rand -base64 48 | tr -d '\n' | tr '/+=' '_-A')

ensure_secret MUNTRA_DB_PASSWORD "$DB_PASSWORD"
ensure_secret MUNTRA_DASHBOARD_TOKEN "$DASHBOARD_TOKEN"

# Read back whatever's stored (so a re-run uses the real value, not the new one we generated this run).
DB_PASSWORD=$(gcloud secrets versions access latest --secret=MUNTRA_DB_PASSWORD --project="$PROJECT_ID")
DASHBOARD_TOKEN=$(gcloud secrets versions access latest --secret=MUNTRA_DASHBOARD_TOKEN --project="$PROJECT_ID")

# ────────────────────────────────────────────────────────────────
# Cloud SQL: instance + database + role
# ────────────────────────────────────────────────────────────────
step "Cloud SQL: ensuring instance $CLOUDSQL_INSTANCE"
if ! gcloud sql instances describe "$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
  info "instance $CLOUDSQL_INSTANCE not found — creating ($CLOUDSQL_TIER, $CLOUDSQL_VERSION, $REGION)"
  info "this takes ~10 minutes"
  gcloud sql instances create "$CLOUDSQL_INSTANCE" \
    --project="$PROJECT_ID" \
    --region="$REGION" \
    --database-version="$CLOUDSQL_VERSION" \
    --tier="$CLOUDSQL_TIER" \
    --storage-type=SSD \
    --storage-size=10 \
    --availability-type=zonal \
    --backup \
    --backup-start-time=03:00 \
    --retained-backups-count=7 \
    --maintenance-window-day=SUN \
    --maintenance-window-hour=04 \
    --quiet
  ok "instance $CLOUDSQL_INSTANCE created"
else
  ok "instance $CLOUDSQL_INSTANCE already exists"
fi

step "Cloud SQL: provisioning database + role on $CLOUDSQL_INSTANCE"

if ! gcloud sql databases describe "$DB_NAME" \
    --instance="$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud sql databases create "$DB_NAME" \
    --instance="$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" >/dev/null
  ok "database $DB_NAME created"
else
  ok "database $DB_NAME already exists"
fi

if ! gcloud sql users list --instance="$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" \
    --format='value(name)' | grep -qx "$DB_ROLE"; then
  gcloud sql users create "$DB_ROLE" \
    --instance="$CLOUDSQL_INSTANCE" \
    --password="$DB_PASSWORD" \
    --project="$PROJECT_ID" >/dev/null
  ok "role $DB_ROLE created"
else
  gcloud sql users set-password "$DB_ROLE" \
    --instance="$CLOUDSQL_INSTANCE" \
    --password="$DB_PASSWORD" \
    --project="$PROJECT_ID" >/dev/null
  ok "role $DB_ROLE present, password synced from Secret Manager"
fi

CLOUDSQL_CONNECTION_NAME=$(gcloud sql instances describe "$CLOUDSQL_INSTANCE" \
  --project="$PROJECT_ID" --format='value(connectionName)')
ok "Cloud SQL connection: $CLOUDSQL_CONNECTION_NAME"

POSTGRES_DSN="postgres://${DB_ROLE}:${DB_PASSWORD}@/${DB_NAME}?host=/cloudsql/${CLOUDSQL_CONNECTION_NAME}&sslmode=disable"
upsert_secret MUNTRA_POSTGRES_DSN "$POSTGRES_DSN"

# ────────────────────────────────────────────────────────────────
# VPC Connector (for Memorystore reach)
# ────────────────────────────────────────────────────────────────
step "Serverless VPC Access Connector"
if gcloud compute networks vpc-access connectors describe "$CONNECTOR_NAME" \
    --region="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  ok "connector $CONNECTOR_NAME already exists"
else
  info "creating $CONNECTOR_NAME (this takes ~2 minutes)"
  gcloud compute networks vpc-access connectors create "$CONNECTOR_NAME" \
    --region="$REGION" \
    --network="$VPC_NETWORK" \
    --range="$CONNECTOR_RANGE" \
    --min-instances=2 \
    --max-instances=3 \
    --project="$PROJECT_ID" >/dev/null
  ok "connector $CONNECTOR_NAME created"
fi

# ────────────────────────────────────────────────────────────────
# Memorystore Redis
# ────────────────────────────────────────────────────────────────
step "Memorystore Redis"
if gcloud redis instances describe "$REDIS_NAME" \
    --region="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  ok "instance $REDIS_NAME already exists"
else
  info "creating $REDIS_NAME (Basic tier ${REDIS_SIZE_GB}GB — takes ~5 minutes)"
  gcloud redis instances create "$REDIS_NAME" \
    --region="$REGION" \
    --tier=basic \
    --size="$REDIS_SIZE_GB" \
    --redis-version="$REDIS_VERSION" \
    --connect-mode=DIRECT_PEERING \
    --network="$VPC_NETWORK" \
    --project="$PROJECT_ID" >/dev/null
  ok "instance $REDIS_NAME created"
fi

REDIS_HOST=$(gcloud redis instances describe "$REDIS_NAME" \
  --region="$REGION" --project="$PROJECT_ID" --format='value(host)')
REDIS_PORT=$(gcloud redis instances describe "$REDIS_NAME" \
  --region="$REGION" --project="$PROJECT_ID" --format='value(port)')
ok "Redis private endpoint: $REDIS_HOST:$REDIS_PORT"
REDIS_URL="redis://${REDIS_HOST}:${REDIS_PORT}/0"
upsert_secret MUNTRA_REDIS_URL "$REDIS_URL"

# ────────────────────────────────────────────────────────────────
# Artifact Registry
# ────────────────────────────────────────────────────────────────
step "Artifact Registry"
if gcloud artifacts repositories describe "$IMAGE_REPO" \
    --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  ok "repo $IMAGE_REPO already exists"
else
  gcloud artifacts repositories create "$IMAGE_REPO" \
    --repository-format=docker \
    --location="$REGION" \
    --description="Muntra analytics service" \
    --project="$PROJECT_ID" >/dev/null
  ok "repo $IMAGE_REPO created"
fi

# ────────────────────────────────────────────────────────────────
# Build + push image
# ────────────────────────────────────────────────────────────────
step "Building image via Cloud Build"
# Build from repo root (this script lives at deploy/heymy-dev/).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
info "context: $REPO_ROOT"
gcloud builds submit "$REPO_ROOT" \
  --tag="$IMAGE" \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --quiet
ok "image pushed: $IMAGE"

# ────────────────────────────────────────────────────────────────
# Grant Cloud Run service account access to secrets
# ────────────────────────────────────────────────────────────────
step "IAM: Cloud Run runtime → Secret Manager"
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')
RUNTIME_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

for s in MUNTRA_POSTGRES_DSN MUNTRA_REDIS_URL MUNTRA_DASHBOARD_TOKEN; do
  gcloud secrets add-iam-policy-binding "$s" \
    --member="serviceAccount:${RUNTIME_SA}" \
    --role=roles/secretmanager.secretAccessor \
    --project="$PROJECT_ID" >/dev/null 2>&1 || true
done
ok "runtime SA ($RUNTIME_SA) granted secretAccessor on the three Muntra secrets"

# ────────────────────────────────────────────────────────────────
# Deploy Cloud Run
# ────────────────────────────────────────────────────────────────
step "Deploying Cloud Run service $SERVICE_NAME"

ENV_VARS="MUNTRA_HTTP_ADDR=:8090"
ENV_VARS="${ENV_VARS},MUNTRA_ALLOWED_SITES=${ALLOWED_SITES}"
if [[ -n "$SITE_ORIGINS" ]]; then
  ENV_VARS="${ENV_VARS},MUNTRA_SITE_ORIGINS=${SITE_ORIGINS}"
fi
ENV_VARS="${ENV_VARS},MUNTRA_FLUSH_INTERVAL=60s,MUNTRA_FLUSH_BATCH_SIZE=500,MUNTRA_ROLLUP_INTERVAL=15m"

gcloud run deploy "$SERVICE_NAME" \
  --image="$IMAGE" \
  --region="$REGION" \
  --project="$PROJECT_ID" \
  --platform=managed \
  --allow-unauthenticated \
  --port=8090 \
  --cpu="$CPU" \
  --memory="$MEMORY" \
  --min-instances="$MIN_INSTANCES" \
  --max-instances="$MAX_INSTANCES" \
  --add-cloudsql-instances="$CLOUDSQL_CONNECTION_NAME" \
  --vpc-connector="$CONNECTOR_NAME" \
  --vpc-egress=private-ranges-only \
  --set-env-vars="$ENV_VARS" \
  --set-secrets="MUNTRA_POSTGRES_DSN=MUNTRA_POSTGRES_DSN:latest,MUNTRA_REDIS_URL=MUNTRA_REDIS_URL:latest,MUNTRA_DASHBOARD_TOKEN=MUNTRA_DASHBOARD_TOKEN:latest" \
  --quiet

URL=$(gcloud run services describe "$SERVICE_NAME" --region="$REGION" --project="$PROJECT_ID" --format='value(status.url)')
ok "Cloud Run service deployed: $URL"

# ────────────────────────────────────────────────────────────────
# Smoke test
# ────────────────────────────────────────────────────────────────
step "Smoke test"
if curl -sf -o /dev/null -w "%{http_code}\n" "$URL/health" | grep -q 200; then
  ok "$URL/health → 200"
else
  warn "$URL/health did not return 200 — check logs:"
  echo "    gcloud run services logs read $SERVICE_NAME --region=$REGION --project=$PROJECT_ID --limit=50"
fi

# ────────────────────────────────────────────────────────────────
# Output: how to wire Heymy
# ────────────────────────────────────────────────────────────────
step "Done. Next: wire Heymy to this service."

cat <<EOF

Run on your workstation, then redeploy the Heymy Cloud Run service:

  echo -n "$DASHBOARD_TOKEN" | \\
    gcloud secrets create MUNTRA_DASHBOARD_TOKEN \\
      --project=$PROJECT_ID \\
      --replication-policy=user-managed \\
      --locations=$REGION \\
      --data-file=- 2>/dev/null \\
    || echo -n "$DASHBOARD_TOKEN" | \\
       gcloud secrets versions add MUNTRA_DASHBOARD_TOKEN \\
         --project=$PROJECT_ID --data-file=-

  gcloud run services update heymy-app \\
    --project=$PROJECT_ID \\
    --region=$REGION \\
    --update-env-vars=MUNTRA_BASE_URL=$URL,MUNTRA_SITE_ID=heymy \\
    --update-secrets=MUNTRA_DASHBOARD_TOKEN=MUNTRA_DASHBOARD_TOKEN:latest

After the Heymy redeploy, every Heymy pageview will hit:
  $URL/script.js   (tracker)
  $URL/collect     (event ingestion)
And the /dashboard/analytics page will read from:
  $URL/api/stats, /api/timeseries, /api/breakdown, /api/live

EOF
