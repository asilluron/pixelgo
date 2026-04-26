#!/usr/bin/env bash
# Idempotent DigitalOcean deploy for pixelgo.
#
# Creates (or reuses) the DOCR registry + Managed Valkey cluster, builds a
# linux/amd64 image, pushes it to DOCR, renders .do/app.yaml.tmpl with
# values from .env + .do/secrets.env, and creates or updates the App
# Platform app. Safe to re-run: all resource lookups are by name.
set -euo pipefail

APP_NAME="${APP_NAME:-pixelgo}"
# App Platform uses short region slugs (sfo, nyc, fra…); Managed DBs +
# DOCR use the numbered slugs (sfo3, nyc3, fra1…). Keep both in sync.
APP_REGION="${APP_REGION:-sfo}"
INFRA_REGION="${INFRA_REGION:-sfo3}"
REGISTRY_NAME="${REGISTRY_NAME:-pixelgo}"
VALKEY_NAME="${VALKEY_NAME:-pixelgo-redis}"
VALKEY_SIZE="${VALKEY_SIZE:-db-s-1vcpu-1gb}"
SECRETS_FILE=".do/secrets.env"
RENDERED_SPEC=".do/rendered-app.yaml"

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m  %s\n' "$*"; }
die() { printf '\033[1;31mxx\033[0m  %s\n' "$*" >&2; exit 1; }

command -v doctl >/dev/null  || die "doctl not installed (brew install doctl)"
command -v docker >/dev/null || die "docker not installed"
command -v envsubst >/dev/null || die "envsubst not installed (brew install gettext)"

[[ -f .env ]] || die ".env missing"
set -a; . ./.env; set +a

: "${DIGITALOCEAN_ACCESS_TOKEN:?missing in .env}"
: "${SUPABASE_URL:?missing in .env}"
: "${SUPABASE_DB_URL:?missing in .env}"
: "${PIXELGO_BOOTSTRAP_EMAIL:?missing in .env}"
: "${PIXELGO_BOOTSTRAP_PASSWORD:?missing in .env}"
export DIGITALOCEAN_ACCESS_TOKEN

# doctl prefers any saved auth context (~/.config/doctl/contexts.yaml) over
# the DIGITALOCEAN_ACCESS_TOKEN env var, which silently routes commands to
# the wrong account when multiple tokens are saved. Force every call to
# use the token from .env.
doctl() { command doctl --access-token "$DIGITALOCEAN_ACCESS_TOKEN" "$@"; }

ACCT_EMAIL="$(doctl account get --format Email --no-header)"
log "DigitalOcean account: $ACCT_EMAIL"

# ---------- production-only secrets (generated once, persisted) ----------
mkdir -p .do
if [[ ! -f "$SECRETS_FILE" ]]; then
  log "Generating production PIXELGO_SESSION_SECRET"
  SS="$(openssl rand -hex 32)"
  printf 'PIXELGO_SESSION_SECRET=%s\n' "$SS" > "$SECRETS_FILE"
  chmod 600 "$SECRETS_FILE"
fi
set -a; . "$SECRETS_FILE"; set +a
: "${PIXELGO_SESSION_SECRET:?generated but not loaded}"

# ---------- DOCR registry ----------
if ! doctl registry get >/dev/null 2>&1; then
  log "Creating DOCR registry: $REGISTRY_NAME ($INFRA_REGION)"
  doctl registry create "$REGISTRY_NAME" --region "$INFRA_REGION" --subscription-tier starter
else
  existing="$(doctl registry get --format Name --no-header)"
  [[ "$existing" == "$REGISTRY_NAME" ]] || warn "DOCR already provisioned as '$existing' (expected '$REGISTRY_NAME'); continuing with '$existing'"
  REGISTRY_NAME="$existing"
fi
doctl registry login >/dev/null

# ---------- Managed Valkey (Redis-compatible) ----------
# Engine slug is `valkey` (Redis 7 OSS replacement); older doctl `--help`
# output forgets to list it but the API accepts it. `databases create` in
# older doctl doesn't take --format, so look the ID up by name post-create.
VALKEY_ID="$(doctl databases list --format ID,Name,Engine --no-header \
  | awk -v n="$VALKEY_NAME" '$2==n && ($3=="redis" || $3=="valkey") {print $1}' | head -n1)"
if [[ -z "$VALKEY_ID" ]]; then
  log "Creating managed Valkey: $VALKEY_NAME ($VALKEY_SIZE, $INFRA_REGION)"
  doctl databases create "$VALKEY_NAME" \
    --engine valkey --size "$VALKEY_SIZE" --region "$INFRA_REGION" --num-nodes 1 \
    --wait >/dev/null
  VALKEY_ID="$(doctl databases list --format ID,Name --no-header \
    | awk -v n="$VALKEY_NAME" '$2==n {print $1}' | head -n1)"
  [[ -n "$VALKEY_ID" ]] || die "Valkey cluster created but could not find its ID"
else
  log "Reusing Valkey cluster: $VALKEY_NAME ($VALKEY_ID)"
fi

log "Waiting for Valkey to be online…"
for _ in $(seq 1 60); do
  status="$(doctl databases get "$VALKEY_ID" --format Status --no-header)"
  [[ "$status" == "online" ]] && break
  sleep 5
done
[[ "$status" == "online" ]] || die "Valkey never came online (last status: $status)"

PIXELGO_REDIS_URL="$(doctl databases connection "$VALKEY_ID" --format URI --no-header)"
export PIXELGO_REDIS_URL

# ---------- Build + push image ----------
IMAGE="registry.digitalocean.com/${REGISTRY_NAME}/pixelgo:latest"
log "Building linux/amd64 image → $IMAGE"
docker buildx build --platform linux/amd64 -t "$IMAGE" --push .

# ---------- Render spec ----------
log "Rendering $RENDERED_SPEC"
: "${PIXELGO_RL_PER_SEC:=200}"
: "${PIXELGO_RL_BURST:=400}"
export PIXELGO_RL_PER_SEC PIXELGO_RL_BURST
envsubst < .do/app.yaml.tmpl > "$RENDERED_SPEC"
chmod 600 "$RENDERED_SPEC"

# ---------- Create or update the App ----------
APP_ID="$(doctl apps list --format ID,Spec.Name --no-header 2>/dev/null \
  | awk -v n="$APP_NAME" '$2==n {print $1}' | head -n1)"
if [[ -z "$APP_ID" ]]; then
  log "Creating App Platform app: $APP_NAME"
  doctl apps create --spec "$RENDERED_SPEC" --wait >/dev/null
  APP_ID="$(doctl apps list --format ID,Spec.Name --no-header \
    | awk -v n="$APP_NAME" '$2==n {print $1}' | head -n1)"
  [[ -n "$APP_ID" ]] || die "App created but could not find its ID"
else
  log "Updating App Platform app: $APP_NAME ($APP_ID)"
  doctl apps update "$APP_ID" --spec "$RENDERED_SPEC" --wait >/dev/null
fi

# ---------- Output next steps ----------
INGRESS="$(doctl apps get "$APP_ID" --format DefaultIngress --no-header)"
cat <<EOF

✓ Deploy complete.

  App ID:        $APP_ID
  Default URL:   $INGRESS
  Registry:      registry.digitalocean.com/$REGISTRY_NAME/pixelgo:latest
  Valkey:        $VALKEY_NAME ($VALKEY_ID)

DNS for pixelgo.cloud:
  Easiest path — delegate DNS to DigitalOcean:
    1. doctl domain create pixelgo.cloud
    2. At your registrar, set NS records to:
         ns1.digitalocean.com
         ns2.digitalocean.com
         ns3.digitalocean.com
    3. The App will auto-issue Let's Encrypt certs within a few minutes.

  Keep DNS elsewhere:
    - CNAME  www.pixelgo.cloud  →  ${INGRESS#https://}
    - ALIAS/ANAME (or A)  pixelgo.cloud  →  ${INGRESS#https://}
      (if your registrar has no ALIAS record type, delegate to DO per above)

Logs:   make do-logs
Status: doctl apps get $APP_ID
EOF
