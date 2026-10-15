#!/usr/bin/env bash
# Prints the GitHub step outputs "ref" and "labels" for one image of this
# repository: ghcr.io/<owner>/<repo><suffix>:<version>, where the version is
# the pushed tag without its leading "v". Used by release.yaml.
#
# Usage: image-ref.sh <suffix>   ("" for the api, -worker, -cerbos)
#
# Refuses a tag that is not vMAJOR.MINOR.PATCH (with an optional
# -prerelease), so a stray tag never publishes an image.
set -euo pipefail

suffix="${1-}"
tag="${GITHUB_REF_NAME:?GITHUB_REF_NAME is not set}"

if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "image-ref.sh: tag '$tag' is not vMAJOR.MINOR.PATCH; refusing to publish" >&2
	exit 1
fi

# Registry paths must be lowercase; GitHub owner and repository names may not be.
repository="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
repository="${repository,,}"
version="${tag#v}"

echo "ref=ghcr.io/${repository}${suffix}:${version}"
{
	echo "labels<<EOF"
	echo "org.opencontainers.image.source=${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY}"
	echo "org.opencontainers.image.revision=${GITHUB_SHA:?GITHUB_SHA is not set}"
	echo "org.opencontainers.image.version=${version}"
	echo "EOF"
}
