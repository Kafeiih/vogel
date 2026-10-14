# Base images are pinned by digest (the multi-arch index digest of the tag
# shown), so a rebuild uses exactly the bytes that were reviewed even if the
# tag is moved upstream. The tag stays for readability. To update: run
#   docker buildx imagetools inspect <image:tag>
# and replace the sha256 with the "Digest:" line of the output.

# Build stage
FROM golang:1.26-alpine3.23@sha256:6750308561953d451210ed1a8086d36c8c8bdcbf672d38d9d0d424b8a7a0216c AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /build/api ./cmd/api

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /build/worker ./cmd/worker

# Runtime stage — API (default)
FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS api

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1001 appgroup \
    && adduser -u 1001 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

COPY --from=builder /build/api .

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/v1/healthz || exit 1

ENTRYPOINT ["./api"]

# Runtime stage — Worker
FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS worker

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1001 appgroup \
    && adduser -u 1001 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

COPY --from=builder /build/worker .

USER appuser

ENTRYPOINT ["./worker"]
