#!/usr/bin/env bash
# Tests image-ref.sh, which decides what release.yaml publishes. Runs in
# vogel's CI (the template workflow itself never runs there) and works as is
# in a generated repository: .github/workflows/image-ref_test.sh
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/image-ref.sh"
failed=0

run() {
	GITHUB_REF_NAME="$1" GITHUB_REPOSITORY="$2" GITHUB_SHA=abc123 \
		GITHUB_SERVER_URL=https://github.com "$SCRIPT" "$3"
}

# expect_ok <tag> <repository> <suffix> <expected name> <expected ref> <expected version>
expect_ok() {
	local out
	if ! out="$(run "$1" "$2" "$3" 2>&1)"; then
		echo "FAIL: $1 $2 '$3' was refused: $out" >&2
		failed=1
		return
	fi
	for want in "name=$4" "ref=$5" "version=$6" "org.opencontainers.image.version=$6" \
		"org.opencontainers.image.source=https://github.com/$2" \
		"org.opencontainers.image.revision=abc123"; do
		if ! grep -qxF "$want" <<<"$out"; then
			echo "FAIL: $1 $2 '$3': missing line '$want' in:" >&2
			echo "$out" >&2
			failed=1
		fi
	done
}

# expect_refused <tag>
expect_refused() {
	if out="$(run "$1" Owner/Repo "" 2>&1)"; then
		echo "FAIL: tag '$1' was accepted: $out" >&2
		failed=1
	fi
}

expect_ok v1.2.3 Kafeiih/My-Service "" ghcr.io/kafeiih/my-service ghcr.io/kafeiih/my-service:1.2.3 1.2.3
expect_ok v1.2.3 Kafeiih/My-Service -worker ghcr.io/kafeiih/my-service-worker ghcr.io/kafeiih/my-service-worker:1.2.3 1.2.3
expect_ok v0.10.0-rc.1 org/svc -cerbos ghcr.io/org/svc-cerbos ghcr.io/org/svc-cerbos:0.10.0-rc.1 0.10.0-rc.1

for tag in v1.2 1.2.3 latest vfoo v1.2.3+build v1.2.3.4 ""; do
	expect_refused "$tag"
done

if ((failed)); then
	echo "image-ref_test.sh: FAILED" >&2
	exit 1
fi
echo "image-ref_test.sh: ok"
