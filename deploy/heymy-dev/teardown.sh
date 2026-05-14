#!/usr/bin/env bash
# Tear down Muntra resources in heymy-dev. Reversible — keeps secrets in
# Secret Manager unless --purge-secrets is passed. Use this to retire the
# deployment cleanly (e.g. when phase 2 ports ingestion into Heymy itself).
#
# Usage:
#   bash deploy/heymy-dev/teardown.sh                # interactive confirm
#   bash deploy/heymy-dev/teardown.sh --purge-secrets

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-muntra-prod}"
REGION="${REGION:-europe-north1}"
SERVICE_NAME="${SERVICE_NAME:-muntra}"
REDIS_NAME="${REDIS_NAME:-muntra-redis}"
CONNECTOR_NAME="${CONNECTOR_NAME:-muntra-connector}"
CLOUDSQL_INSTANCE="${CLOUDSQL_INSTANCE:-muntra-db}"
DB_NAME="${DB_NAME:-muntra}"
DB_ROLE="${DB_ROLE:-muntra}"

PURGE_SECRETS=false
DELETE_SQL_INSTANCE=false
for arg in "$@"; do
  case "$arg" in
    --purge-secrets)      PURGE_SECRETS=true ;;
    --delete-sql-instance) DELETE_SQL_INSTANCE=true ;;
    *) echo "Unknown flag: $arg"; exit 2 ;;
  esac
done

ok()   { printf "  \033[32m✓\033[0m %s\n" "$*"; }
info() { printf "  \033[36m→\033[0m %s\n" "$*"; }
step() { printf "\n\033[1m%s\033[0m\n" "$*"; }

cat <<EOF
Tearing down Muntra from $PROJECT_ID / $REGION:
  - Cloud Run service:  $SERVICE_NAME
  - Memorystore Redis:  $REDIS_NAME   (\$35/mo cost stops here)
  - VPC Connector:      $CONNECTOR_NAME (\$8-12/mo cost stops here)
  - Cloud SQL database: $DB_NAME on $CLOUDSQL_INSTANCE  (data deleted)
  - Cloud SQL role:     $DB_ROLE
  - Secrets:            $($PURGE_SECRETS && echo "DELETED" || echo "KEPT (use --purge-secrets to remove)")

EOF
read -r -p "Type 'destroy' to confirm: " ans
[[ "$ans" == "destroy" ]] || { echo "Aborted."; exit 1; }

step "Cloud Run"
gcloud run services delete "$SERVICE_NAME" --region="$REGION" --project="$PROJECT_ID" --quiet 2>/dev/null \
  && ok "service $SERVICE_NAME deleted" || info "service $SERVICE_NAME absent"

step "Memorystore Redis"
gcloud redis instances delete "$REDIS_NAME" --region="$REGION" --project="$PROJECT_ID" --quiet 2>/dev/null \
  && ok "instance $REDIS_NAME deleted" || info "instance $REDIS_NAME absent"

step "VPC Connector"
gcloud compute networks vpc-access connectors delete "$CONNECTOR_NAME" \
  --region="$REGION" --project="$PROJECT_ID" --quiet 2>/dev/null \
  && ok "connector $CONNECTOR_NAME deleted" || info "connector $CONNECTOR_NAME absent"

step "Cloud SQL database + role"
gcloud sql databases delete "$DB_NAME" --instance="$CLOUDSQL_INSTANCE" \
  --project="$PROJECT_ID" --quiet 2>/dev/null \
  && ok "database $DB_NAME dropped (data gone)" || info "database $DB_NAME absent"
gcloud sql users delete "$DB_ROLE" --instance="$CLOUDSQL_INSTANCE" \
  --project="$PROJECT_ID" --quiet 2>/dev/null \
  && ok "role $DB_ROLE removed" || info "role $DB_ROLE absent"

if $DELETE_SQL_INSTANCE; then
  step "Cloud SQL instance (--delete-sql-instance)"
  gcloud sql instances delete "$CLOUDSQL_INSTANCE" --project="$PROJECT_ID" --quiet 2>/dev/null \
    && ok "instance $CLOUDSQL_INSTANCE deleted (~\$7-8/mo cost stops here)" \
    || info "instance $CLOUDSQL_INSTANCE absent"
else
  info "instance $CLOUDSQL_INSTANCE kept — use --delete-sql-instance to remove it (only if NOT shared with other workloads)"
fi

if $PURGE_SECRETS; then
  step "Secrets"
  for s in MUNTRA_DB_PASSWORD MUNTRA_POSTGRES_DSN MUNTRA_REDIS_URL MUNTRA_DASHBOARD_TOKEN; do
    gcloud secrets delete "$s" --project="$PROJECT_ID" --quiet 2>/dev/null \
      && ok "$s deleted" || info "$s absent"
  done
fi

echo
ok "Teardown complete. Remember to clear Heymy's MUNTRA_* env vars too:"
echo "    gcloud run services update heymy-app --region=$REGION --project=$PROJECT_ID \\"
echo "      --remove-env-vars=MUNTRA_BASE_URL,MUNTRA_SITE_ID \\"
echo "      --remove-secrets=MUNTRA_DASHBOARD_TOKEN"
