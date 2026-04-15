# syntax=docker/dockerfile:1.7

# Stage 1: build the single teams_con binary.
FROM golang:1.25-alpine AS builder

# Build-time version injected via --build-arg VERSION=v1.0.0. Defaults
# to "dev" for ad-hoc docker compose builds where provenance is not
# needed. Set by release builds / CI.
ARG VERSION=dev

WORKDIR /src

# Dependency layer — cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Source.
COPY . .

# Static binary, stripped, reproducible paths, build version injected.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X teams_con/internal/version.Version=${VERSION}" \
    -o /out/teams_con .

# Stage 2: minimal runtime. Distroless static + nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/teams_con /teams_con

USER nonroot:nonroot

# Compose supplies the subcommand (crawl | serve | tui) via `command:`.
ENTRYPOINT ["/teams_con"]
