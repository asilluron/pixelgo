# syntax=docker/dockerfile:1.7

# ---------- build ----------
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads separately from source for fast incremental builds.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# Static, stripped binary. CGO is off because we have no native deps.
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -trimpath -ldflags="-s -w" -o /out/pixelgo ./cmd/pixelgo

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/pixelgo /app/pixelgo

# /healthz is served by the app; orchestrators should probe it.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/pixelgo"]
