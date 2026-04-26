.PHONY: run build test test-integration vet tidy fmt \
        redis-up redis-down \
        docker-build docker-run \
        dev-up dev-down dev-logs \
        do-deploy do-logs do-status do-destroy \
        db-push openapi-validate og-image

BINARY := bin/pixelgo
IMAGE  ?= pixelgo:dev

run:
	go run ./cmd/pixelgo

build:
	mkdir -p bin
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/pixelgo

test:
	go test ./...

# Exercises real Supabase + Redis; requires a populated .env.
test-integration:
	go test -tags=integration ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

redis-up:
	docker compose up -d redis

redis-down:
	docker compose down

# Container image. Multi-stage Dockerfile produces a distroless binary.
docker-build:
	docker build -t $(IMAGE) .

# Bring up the full stack (redis + pixelgo) locally.
docker-run:
	docker compose --profile app up --build

# Hot-reload dev stack: redis + pixelgo-dev. Templates under web/templates are
# re-read on every request, so UI edits are visible without rebuilding.
dev-up:
	docker compose --profile dev up -d

dev-down:
	docker compose --profile dev down

dev-logs:
	docker compose --profile dev logs -f pixelgo-dev

# ---------- DigitalOcean App Platform ----------
# First run provisions DOCR + Managed Valkey + the App; subsequent runs
# just rebuild + push the image (App Platform redeploys on DOCR push).
do-deploy:
	./scripts/do-deploy.sh

# Tail live logs from the running App Platform instance.
# Sources .env and forces --access-token so doctl ignores any saved auth
# contexts that point at unrelated DigitalOcean accounts.
do-logs:
	@set -a; . ./.env; set +a; \
	 APP_ID=$$(doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps list --format ID,Spec.Name --no-header | awk '$$2=="pixelgo"{print $$1}'); \
	 [ -n "$$APP_ID" ] || { echo "app 'pixelgo' not found — run make do-deploy first"; exit 1; }; \
	 doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps logs $$APP_ID --type run --follow

# Print current deployment status + default ingress URL.
do-status:
	@set -a; . ./.env; set +a; \
	 APP_ID=$$(doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps list --format ID,Spec.Name --no-header | awk '$$2=="pixelgo"{print $$1}'); \
	 [ -n "$$APP_ID" ] || { echo "app 'pixelgo' not found"; exit 1; }; \
	 doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps get $$APP_ID

# Tear down the App + Valkey cluster. DOCR registry and pushed images are
# kept (registry teardown is destructive across other apps). Requires
# CONFIRM=yes to guard against accidents.
do-destroy:
	@test "$(CONFIRM)" = "yes" || { echo "refusing: run with CONFIRM=yes to tear down prod"; exit 1; }
	@set -a; . ./.env; set +a; \
	 APP_ID=$$(doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps list --format ID,Spec.Name --no-header | awk '$$2=="pixelgo"{print $$1}'); \
	 DB_ID=$$(doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN databases list --format ID,Name --no-header | awk '$$2=="pixelgo-redis"{print $$1}'); \
	 [ -n "$$APP_ID" ] && doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN apps delete $$APP_ID --force || echo "no pixelgo app"; \
	 [ -n "$$DB_ID" ] && doctl --access-token $$DIGITALOCEAN_ACCESS_TOKEN databases delete $$DB_ID --force || echo "no pixelgo-redis db"

# Apply incremental migrations under supabase/migrations/ to the linked project.
# For a fresh project, paste supabase/schema.sql into the SQL editor first.
db-push:
	supabase db push

# Regenerate the 1200x630 OpenGraph image referenced from web/templates/index.html.
# Pure stdlib generator — see cmd/genogimage. Edit web/static/og.svg for the
# source-of-truth design and rerun this target to refresh the PNG.
og-image:
	go run ./cmd/genogimage -out web/static/og.png

# Lightweight OpenAPI sanity check — runs the spectral linter via Docker if
# available, otherwise just parses the YAML via `yq` / `python -c yaml.safe_load`.
openapi-validate:
	@if command -v spectral >/dev/null; then \
	   spectral lint api/openapi.yaml; \
	else \
	   python3 -c 'import yaml,sys; yaml.safe_load(open("api/openapi.yaml"))' \
	     && echo "api/openapi.yaml: parses as YAML (install spectral for full lint)"; \
	fi
