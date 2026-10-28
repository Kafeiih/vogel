# Cerbos PDP image with this service's policies baked in.
#
# The final stage copies the policies FROM the validate stage, so BuildKit
# cannot produce an image without first compiling the policies and running
# their tests: an image that exists is an image whose policies passed. See
# README.md for what the build does not prove (smoke.sh covers that).
#
# The Cerbos version is pinned on the base stage (tag plus digest, so
# Dependabot can bump it). deploy/docker-compose.developer.yaml pins the same
# image for local development: keep the two equal. The upstream image is
# FROM scratch: no shell, so RUN must use the exec form.
FROM ghcr.io/cerbos/cerbos:0.57.0@sha256:e5e3b150b3cb0090975605254f255164567d67e233634199fb20972e3eaff300 AS base

FROM base AS validate
COPY policies /repo/policies
COPY tests /repo/tests
RUN ["/cerbos", "compile", "/repo"]

FROM base
COPY .cerbos.yaml /config/.cerbos.yaml
# Not /policies: the upstream image declares it a VOLUME, and docker compose
# carries a container's anonymous volumes over when it recreates it, so a new
# image would keep serving the previous image's policies (a revoked
# permission would stay granted). /bundle is not a volume, so the policies
# served are always this image's.
COPY --from=validate /repo/policies /bundle/policies
# The upstream image runs as root. The PDP only reads /config and
# /bundle/policies, so it runs as an unprivileged, nameless UID (there is no
# /etc/passwd).
USER 65532:65532
CMD ["server", "--config=/config/.cerbos.yaml"]
