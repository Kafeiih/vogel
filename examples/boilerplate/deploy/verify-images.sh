#!/usr/bin/env bash
# Pulls the images of deploy/docker-compose.yaml and checks the cosign
# signature of every image not pinned by digest (the service's own images,
# referenced by APP_VERSION), against the public key of the repository.
# Images pinned by digest (third-party ones such as postgres) are fixed by
# their hash and need no signature.
#
# The signature must also carry the image's tag as its "version"
# annotation (release.yaml adds it), so a tag moved to an older signed digest
# is refused too.
#
# Each image is verified by the digest that was just pulled, so
# `docker compose up -d` afterwards runs exactly what was verified: it does
# not pull again an image that is already local.
#
#   deploy/verify-images.sh && docker compose -f deploy/docker-compose.yaml up -d
#
# Environment:
#   COSIGN_PUBLIC_KEY  public key file (default: deploy/cosign.pub)
#   IMAGE_VERIFY       "on" (default) or "off" to pull without checking, for
#                      images published before signing existed; it prints a
#                      warning, and deploys the local images if the pull
#                      fails
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$root/deploy/docker-compose.yaml"
key="${COSIGN_PUBLIC_KEY:-$root/deploy/cosign.pub}"

fail() {
	echo "verify-images: $*" >&2
	exit 1
}

case "${IMAGE_VERIFY:-on}" in
on) ;;
off)
	# Still pull, so a rollback downloads before the service goes down; with
	# the registry down, fall back to the images already on the host.
	echo "verify-images: WARNING: IMAGE_VERIFY=off, deploying images without checking their signatures" >&2
	docker compose -f "$compose_file" pull --quiet ||
		echo "verify-images: WARNING: pull failed, using the images already on this host" >&2
	exit 0
	;;
*) fail "IMAGE_VERIFY must be on or off, got '${IMAGE_VERIFY}'" ;;
esac

[ -f "$key" ] || fail "public key $key not found: commit the release key's cosign.pub there or set COSIGN_PUBLIC_KEY (see README, \"Publicar imágenes y CI\")"
command -v cosign >/dev/null || fail "cosign is not installed (see README, \"Publicar imágenes y CI\")"

images="$(docker compose -f "$compose_file" config --images | sort -u)"
docker compose -f "$compose_file" pull --quiet

verified=0
while IFS= read -r image; do
	[ -n "$image" ] || continue
	case "$image" in
	*@sha256:*) continue ;;
	esac

	# Strip the tag: the last ':' after the last '/' (a registry port such
	# as host:5000/name has its ':' before a '/').
	repo="$image"
	last="${image##*/}"
	[[ "$last" == *:* ]] || fail "$image: no version tag to check the signature against"
	repo="${image%:*}"
	version="${image##*:}"

	# Docker records Docker Hub repositories by their short name
	# (docker.io/library/redis -> redis), so match that form.
	short="${repo#docker.io/}"
	short="${short#index.docker.io/}"
	short="${short#library/}"

	# Exact prefix match: only this repository's digest, never one whose
	# name merely contains it.
	ref="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image" |
		awk -v prefix="${short}@sha256:" 'index($0, prefix) == 1 { print; exit }')"
	[ -n "$ref" ] || fail "$image: no registry digest after pulling it"

	if ! out="$(cosign verify --key "$key" --insecure-ignore-tlog=true \
		-a "version=$version" "$ref" 2>&1)"; then
		echo "$out" >&2
		fail "$image ($ref): signature check failed, do not deploy it"
	fi
	echo "verify-images: $image ($ref) signed"
	verified=$((verified + 1))
done <<<"$images"

echo "verify-images: $verified image(s) verified"
