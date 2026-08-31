# Build stage
FROM golang:1.26-alpine AS builder

ARG VERSION=dev

WORKDIR /workspace

# Copy source files
COPY go.mod go.mod
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY internal/ internal/

# Install git and ca-certificates for module downloads
RUN apk add --no-cache git ca-certificates

# Set Go module replace directives directly in the build
RUN go build \
    -mod=mod \
    -buildvcs=false \
    -ldflags="-w -s -X main.version=${VERSION}" \
    -o manager \
    -work \
    main.go 2>&1 || \
    (go get -d ./... && go build -a -ldflags="-w -s -X main.version=${VERSION}" -o manager main.go)

# Final stage
FROM alpine:3.18

WORKDIR /

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates openssl

COPY --from=builder /workspace/manager .

# Use existing non-root user (nobody/65534 in alpine)
USER 65532:65532

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8081/healthz || exit 1

EXPOSE 8080 8081 9443

ENTRYPOINT ["/manager"]
