# Build stage
FROM golang:1.27-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS builder

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
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

WORKDIR /

COPY --from=builder /workspace/manager .

USER 65532:65532

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/bin/sh", "-c", "wget --no-verbose --tries=1 --spider http://localhost:8081/healthz || exit 1"]

EXPOSE 8080 8081 9443

ENTRYPOINT ["/manager"]
