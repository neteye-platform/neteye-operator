# Build stage
FROM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

ARG VERSION=dev

WORKDIR /workspace

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
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

WORKDIR /

COPY --from=builder /workspace/manager .

USER 65532:65532

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/bin/sh", "-c", "wget --no-verbose --tries=1 --spider http://localhost:8081/healthz || exit 1"]

EXPOSE 8080 8081 9443

ENTRYPOINT ["/manager"]
