# ── build stage ──────────────────────────────────────────────
# Digest-pinned so a build today and a build next year use byte-identical
# base layers; a tag alone (golang:1.25-alpine) can move under you. Pin
# corresponds to golang:1.25-alpine as of 2026-08-17 — see
# DEPLOY.md "供应链: 基础镜像 digest" for the bump procedure and review cadence.
FROM golang:1.25-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946 AS build
WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO off keeps the binary static — that is what lets the runtime stage be
# this thin and the process run with a read-only root filesystem.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/geoverse ./cmd/geoverse

# ── runtime stage ────────────────────────────────────────────
# Pin corresponds to alpine:3.20 as of 2026-08-17; same bump procedure as
# the build stage above.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

# ca-certificates is needed for TLS to a remote PostGIS; wget (busybox) backs
# the HEALTHCHECK below. Both are already in alpine's base or apk cache.
RUN apk add --no-cache ca-certificates

# Numeric UID, not just a name: Kubernetes' runAsNonRoot admission check
# cannot verify a username against the image, so a named-only USER is
# rejected by `securityContext.runAsNonRoot: true`.
RUN adduser -D -H -u 10001 geoverse

COPY --from=build /out/geoverse /usr/local/bin/geoverse

USER 10001:10001
EXPOSE 8080

# Probes the readiness endpoint, which is exempt from API-key auth precisely
# so that infrastructure can reach it. Port is the in-container default; if
# you override server.port in config, override this too.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1

ENTRYPOINT ["geoverse"]
CMD ["-config", "/etc/geoverse/config.yaml"]
