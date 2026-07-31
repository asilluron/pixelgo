# pixelgo

A single-purpose, high-throughput tracking-pixel server written in Go.

Serves a 1×1 transparent GIF at `GET /p/{pixelID}` and atomically increments a
per-pixel view counter in Redis. An admin UI at `/admin` lets owners and
super-admins manage pixels, invites, and API keys. Customers pull their data
and stats over a versioned JSON API at `/api/v1`.

- **Language:** Go 1.25
- **Framework:** [Echo](https://echo.labstack.com/)
- **Storage:** Redis (pixel metadata + counters) + Supabase Postgres (orgs,
  memberships, invites, profiles, API keys)
- **Auth:** Supabase Auth for the UI (cookie/JWT); bearer API keys for the API
- **UI:** server-rendered `html/template` + Tailwind (CDN)
- **License:** MIT — © 2026 Andrew Silluron

## Architecture

```
          ┌────────────┐           ┌───────────────┐
  GET /p  │            │  INCR →   │               │
  ───────▶│  pixelgo   │──────────▶│     Redis     │
          │  (Echo)    │           │ counters +    │
  /admin  │            │           │ pixel metadata│
  /api/v1 │            │           └───────────────┘
          │            │           ┌───────────────┐
          │            │──────────▶│   Supabase    │
          └────────────┘   pgx     │ Auth + orgs + │
                                   │ api_keys      │
                                   └───────────────┘
```

Hot path (`GET /p/:id`) is a single `INCR` plus three `EXPIRE` calls in one
pipelined round-trip, served asynchronously so client latency never waits on
Redis. Everything else (admin UI, API) runs on the standard request path.

## Quick start (local dev)

```bash
# 1. Redis
make redis-up

# 2. Env
cp .env.example .env
# Fill in SUPABASE_* values for a real or self-hosted Supabase project and
# set PIXELGO_SESSION_SECRET + PIXELGO_BOOTSTRAP_{EMAIL,PASSWORD}.

# 3. Apply the baseline schema to your Supabase project (one-time, only for
#    fresh projects — skip if you already have orgs/members/invites/profiles).
psql "$SUPABASE_DB_URL" -f supabase/schema.sql

# 4. Apply incremental migrations (api_keys, future changes).
make db-push

# 5. Run
make run
```

Open <http://localhost:8080/admin> and log in with the bootstrap credentials.

## Deploy

### Docker

A multi-stage `Dockerfile` produces a ~20 MB distroless image.

```bash
make docker-build                 # builds pixelgo:dev
docker compose --profile app up   # redis + pixelgo together
```

For production, push the image to your registry and run behind a TLS
terminator. Point `PIXELGO_REDIS_URL` at a managed instance (`rediss://` for
TLS) and `SUPABASE_DB_URL` at Supabase's transaction pooler (port 6543,
URL-encoded password).

### Bare binary

```bash
make build
./bin/pixelgo
```

Templates and the OpenAPI spec are embedded via `go:embed`; no runtime files
are needed.

### Health

`GET /healthz` returns 200 when both Redis and Postgres ping successfully,
503 otherwise. Orchestrator liveness and readiness probes should use it.

## Embedding the pixel

```html
<img src="https://your-host.example.com/p/PIXEL_ID" width="1" height="1"
     alt="" style="position:absolute;left:-9999px" />
```

## API

v1 lives under `/api/v1`. Full contract in
[`api/openapi.yaml`](./api/openapi.yaml); a Swagger UI is mounted at
[`/docs`](http://localhost:8080/docs) on any running instance (also linked
from the admin dashboard header).

### Authentication

`Authorization: Bearer pxg_<kind>_<payload>` — no cookies, no Supabase JWTs.
Two key flavours:

- **Personal** (`pxg_pk_…`) — any org member can mint one for themselves.
  Scope tracks the owner's current org membership: revoke the membership and
  the key silently loses access.
- **Org** (`pxg_ok_…`) — owners/super-admins only. Bound to the org itself,
  independent of any user.

Mint keys from the admin dashboard ("API keys" section). The plaintext
token is shown once on creation and never stored.

### Endpoints

| Method | Path | Notes |
| ------ | ---- | ----- |
| `GET` | `/api/v1/me` | Resolve the caller's token + org |
| `GET` | `/api/v1/org` | Org record the token is scoped to |
| `GET` | `/api/v1/pixels` | Catalog listing: `q` (name prefix), `tag`, `status=active\|deleted`, `sort`, `limit`, `offset` |
| `POST` | `/api/v1/pixels` | Create a pixel dynamically (`name` required; optional `url`, `tags`) |
| `GET` | `/api/v1/pixels/{id}` | Single pixel metadata |
| `DELETE` | `/api/v1/pixels/{id}` | Soft delete: stops counting immediately, expunged after 30 days |
| `GET` | `/api/v1/pixels/{id}/stats` | Lifetime / today / last-hour counters |
| `GET` | `/api/v1/pixels/{id}/timeseries` | Per-bucket counts (`granularity=hour\|day`, `window=N`) |
| `GET` | `/api/v1/pixels:batchStats?ids=a,b,c` | Bulk counters (≤200 ids) |

Write endpoints (`POST`/`DELETE`) require an org key or a personal key whose
owner has the owner/editor role; viewer keys are read-only.

### Deletion & retention

`DELETE /api/v1/pixels/{id}` (or the dashboard's Delete button) is a soft
delete: the pixel vanishes from live listings and its `/p/{id}` hits stop
counting immediately. Metadata and counters are retained for 30 days —
inspect them with `GET /api/v1/pixels?status=deleted` — then a background
worker permanently expunges everything.

### Catalog at scale

Listing is backed by Redis sorted-set indexes (created-at, lexicographic
name, per-tag), so sort/filter/pagination stay O(log N + page) even for orgs
with thousands of pixels. The dashboard exposes the same catalog: search,
tag filter, five sort orders, and 20-per-page pagination.

Responses are wrapped:

```json
{ "data": { ... }, "meta": { ... } }
```

Errors use a stable machine-readable `code`:

```json
{ "error": { "code": "not_found", "message": "pixel not found" } }
```

### Example

```bash
curl -H "Authorization: Bearer $PIXELGO_TOKEN" \
     https://your-host.example.com/api/v1/pixels | jq
```

## Development

```bash
make test              # fast unit tests (miniredis, stub AuthStore)
make test-integration  # hits real Supabase + Redis; needs .env
make vet
make openapi-validate  # lint api/openapi.yaml (uses spectral if installed)
```

Integration tests cover signup/invite flow and the full API-key roundtrip
(mint → `/api/v1/*` → revoke → 401).

## Project layout

```
cmd/pixelgo/              program entrypoint
api/                      OpenAPI 3 spec + go:embed wrapper
internal/config/          env-driven configuration
internal/models/          domain types (Org, User, Pixel, APIKey, …)
internal/store/           PixelStore (Redis) + AuthStore (Postgres) impls
internal/server/          Echo handlers, middleware, templates wiring
internal/server/api/      /api/v1 handlers + bearer-key middleware
internal/supaauth/        thin Supabase Auth client + JWT verifier
supabase/schema.sql       one-shot baseline for fresh Supabase projects
supabase/migrations/      incremental migrations (Supabase CLI)
web/templates/            html/template files (embedded at build time)
```

## Roadmap

Short list of things that are deliberately out of scope for v1 but are
natural next steps:

- Redis-backed token bucket rate limiting on `/api/v1/*` (per key rather
  than per IP) — currently unlimited.
- Pixel rename + restore-from-soft-delete endpoints.
- Webhooks on configurable thresholds.
- Long-term aggregate storage (e.g. a Postgres rollup replica) for windows
  older than the Redis TTLs (72 h hourly, 35 d daily).

