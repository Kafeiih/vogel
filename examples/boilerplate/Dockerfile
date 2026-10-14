# Build context: the repository root, not this directory. The boilerplate's
# go.mod replaces github.com/kafeiih/vogel with ../.., so the builder needs
# the library's sources next to the boilerplate's, as in the repository:
#
#   docker build -f examples/boilerplate/Dockerfile --target api .
#   docker build -f examples/boilerplate/Dockerfile --target worker .
#
# (run from the repository root; deploy/docker-compose.yaml does the same).
# What enters the context is filtered by the root .dockerignore.
#
# APP_DIR is where the service's module lives inside the context. In a copy
# of this boilerplate whose go.mod no longer has the replace (see README,
# "Cómo copiar este boilerplate"), the module is the context root: build
# with --build-arg APP_DIR=. from that directory, or change the default.
#
# Base images are pinned by digest (the multi-arch index digest of the tag
# shown), so a rebuild uses exactly the bytes that were reviewed even if the
# tag is moved upstream. The tag stays for readability. To update: run
#   docker buildx imagetools inspect <image:tag>
# and replace the sha256 with the "Digest:" line of the output. Dependabot
# (.github/dependabot.yml) proposes these bumps weekly.

# Build stage
FROM golang:1.26-alpine3.23@sha256:6750308561953d451210ed1a8086d36c8c8bdcbf672d38d9d0d424b8a7a0216c AS builder

RUN apk add --no-cache git ca-certificates

ARG APP_DIR=examples/boilerplate

# Module files first, so the download layer is reused until a go.mod or
# go.sum changes. Both modules' files are needed: resolving the replace reads
# the library's go.mod. (With APP_DIR=. both lines copy the same files.)
WORKDIR /src
COPY go.mod go.sum ./
COPY ${APP_DIR}/go.mod ${APP_DIR}/go.sum ./${APP_DIR}/

WORKDIR /src/${APP_DIR}
RUN go mod download

# Library and service sources.
COPY . /src

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# Runtime stage — API (default)
FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS api

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1001 appgroup \
    && adduser -u 1001 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

COPY --from=builder /out/api .

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

COPY --from=builder /out/worker .

USER appuser

ENTRYPOINT ["./worker"]
