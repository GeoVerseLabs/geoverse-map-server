# ── build stage ──────────────────────────────────────────────
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/geoverse ./cmd/geoverse

# ── runtime stage ────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H geoverse
USER geoverse
COPY --from=build /out/geoverse /usr/local/bin/geoverse
EXPOSE 8080
ENTRYPOINT ["geoverse"]
CMD ["-config", "/etc/geoverse/config.yaml"]
