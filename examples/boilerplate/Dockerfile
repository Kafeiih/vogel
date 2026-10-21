# Build context: the repository root. APP_DIR is where the service's module
# lives inside the context.
# vogel:only begin
#
# Inside vogel that is vogel's root, not this directory. The boilerplate's
# go.mod replaces github.com/kafeiih/vogel with ../.., so the builder needs
# the library's sources next to the boilerplate's, as in the repository:
#
#   docker build -f examples/boilerplate/Dockerfile --target api .
#   docker build -f examples/boilerplate/Dockerfile --target worker .
#
# (run from the repository root; deploy/docker-compose.yaml does the same).
# What enters the context is filtered by the root .dockerignore.
#
# In a copy of this boilerplate whose go.mod no longer has the replace (see
# README, "Cómo copiar este boilerplate"), the module is the context root:
# build with --build-arg APP_DIR=. from that directory, or change the
# default.
# vogel:only end
#
# Base images are pinned by digest (the multi-arch index digest of the tag
# shown), so a rebuild uses exactly the bytes that were reviewed even if the
# tag is moved upstream. The tag stays for readability. To update: run
#   docker buildx imagetools inspect <image:tag>
# and replace the sha256 with the "Digest:" line of the output. Dependabot
# (.github/dependabot.yml) proposes these bumps weekly.

# Build stage
FROM golang:1.27-alpine3.23@sha256:895e5feab11a4b3774d24ee3c9579028456b82ad37f42a9eb6f82adaf776a201 AS builder

RUN apk add --no-cache git ca-certificates

ARG APP_DIR=examples/boilerplate

# Module files first, so the download layer is reused until a go.mod or
# go.sum changes. (With APP_DIR=. both lines copy the same files.)
# vogel:only begin
# Both modules' files are needed: resolving the replace reads the library's
# go.mod.
# vogel:only end
WORKDIR /src
COPY go.mod go.sum ./
COPY ${APP_DIR}/go.mod ${APP_DIR}/go.sum ./${APP_DIR}/

WORKDIR /src/${APP_DIR}
RUN go mod download

# Service sources (and, inside vogel, the library's).
COPY . /src

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# vogel:component worker begin
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# vogel:component worker end
# Runtime stage — API (default)
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS api

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
# vogel:component worker begin

# Runtime stage — Worker
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS worker

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1001 appgroup \
    && adduser -u 1001 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

COPY --from=builder /out/worker .

USER appuser

# The worker serves no port: "worker server" refreshes a heartbeat file in
# the temp directory while its job queue runs, and "worker healthcheck"
# fails when that file is missing or stale (see cmd/worker/heartbeat.go).
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
    CMD ["./worker", "healthcheck"]

ENTRYPOINT ["./worker"]
# vogel:component worker end
