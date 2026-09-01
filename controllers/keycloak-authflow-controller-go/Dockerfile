# =============================================================================
# Build stage
# =============================================================================
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy dependency files first for Docker layer caching
COPY go.mod go.sum ./

RUN go mod download

# Copy source
COPY main.go .

# Build a static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /keycloak-authflow-controller main.go


# =============================================================================
# Runtime stage
# =============================================================================
FROM alpine:3.22

WORKDIR /app

# CA certificates are needed for HTTPS communication with Keycloak/Kubernetes
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 controller \
    && adduser -S -D -H -u 10001 -G controller controller

COPY --from=builder /keycloak-authflow-controller /app/keycloak-authflow-controller

# Run as non-root
USER 10001:10001

ENTRYPOINT ["/app/keycloak-authflow-controller"]