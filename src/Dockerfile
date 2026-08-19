# Build stage
FROM golang:1.26-alpine AS builder

ARG VERSION=dev

WORKDIR /workspace

# Install git and ca-certificates for module downloads
# hadolint ignore=DL3018
RUN apk add --no-cache git ca-certificates

# Download modules first so this layer is cached unless go.mod/go.sum change
COPY go.mod go.sum ./
RUN go mod download

# Copy source files
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY internal/ internal/

RUN go build \
    -buildvcs=false \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -o manager \
    main.go

# Final stage
FROM alpine:3.18

WORKDIR /

# Install CA certificates for HTTPS
# hadolint ignore=DL3018
RUN apk --no-cache add ca-certificates openssl

COPY --from=builder /workspace/manager .

# Use existing non-root user (nobody/65534 in alpine)
USER 65532:65532

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/bin/sh", "-c", "wget --no-verbose --tries=1 --spider http://localhost:8081/healthz || exit 1"]

EXPOSE 8080 8081 9443

ENTRYPOINT ["/manager"]
