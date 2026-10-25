#!/usr/bin/env bash
# Signs one pushed image digest with the repository's cosign key (see
# "Publicar imágenes y CI" in the README). Used by every image job of
# release.yaml; sign-image_test.sh pins its behavior.
#
# The empty signing config keeps the signature out of the public transparency
# log, so a private service publishes nothing about itself; verify with
# --insecure-ignore-tlog. The version annotation binds the signature to this
# release, so a tag moved to an older signed digest fails
# deploy/verify-images.sh.
#
# Environment:
#   IMAGE               <name>@sha256:<digest> to sign
#   VERSION             release version, stored as the "version" annotation
#   COSIGN_PRIVATE_KEY  the private key (cosign reads it from the environment)
#   COSIGN_PASSWORD     its password
#   RUNNER_TEMP         directory for the signing config
set -euo pipefail

image="${IMAGE:?IMAGE is not set}"
version="${VERSION:?VERSION is not set}"
if [ -z "${COSIGN_PRIVATE_KEY-}" ]; then
	echo "::error::COSIGN_PRIVATE_KEY is not set: create the key pair and the repository secrets described in the README before releasing" >&2
	exit 1
fi

config="${RUNNER_TEMP:?RUNNER_TEMP is not set}/signing-config.json"
cosign signing-config create --out "$config"
cosign sign --yes --key env://COSIGN_PRIVATE_KEY \
	--signing-config "$config" \
	-a "version=$version" "$image"
