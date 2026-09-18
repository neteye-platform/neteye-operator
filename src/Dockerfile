# Build stage
FROM golang:1.27-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS builder

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
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

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
