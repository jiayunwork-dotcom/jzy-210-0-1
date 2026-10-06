# Build stage.
FROM golang:1.23-alpine AS build

WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary (CGO not needed: the MySQL driver is pure Go).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/balancer ./cmd/balancer

# Runtime stage.
FROM alpine:3.20

RUN adduser -D -H -u 10001 app && \
    apk add --no-cache ca-certificates
USER app
WORKDIR /app

COPY --from=build /out/balancer /app/balancer
COPY --chown=app:app migrations /app/migrations

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --retries=10 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/balancer"]
