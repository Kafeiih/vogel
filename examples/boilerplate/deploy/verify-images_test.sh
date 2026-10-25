#!/usr/bin/env bash
# Tests verify-images.sh with stand-ins for docker and cosign on PATH. Runs in
# vogel's CI and works as is in a generated repository:
# deploy/verify-images_test.sh
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/verify-images.sh"
failed=0

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
touch "$tmp/cosign.pub"

# docker: `compose config --images` prints $STUB_IMAGES, `compose pull` is
# logged (and fails when $STUB_PULL_FAIL is set), `image inspect` answers
# <repo>@sha256:<digest of the repo> unless the image is in $STUB_NO_DIGEST.
# Like docker, it records Docker Hub repositories by their short name
# (docker.io/library/redis -> redis).
cat >"$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
if [ "$1" = compose ]; then
	case "$*" in
	*"config --images"*) printf '%s\n' $STUB_IMAGES ;;
	*pull*) [ -z "$STUB_PULL_FAIL" ] || { echo "Error: registry unreachable" >&2; exit 1; } ;;
	esac
	exit 0
fi
if [ "$1 $2" = "image inspect" ]; then
	image="${!#}"
	case " $STUB_NO_DIGEST " in *" $image "*) exit 0 ;; esac
	repo="$image"
	last="${image##*/}"
	[[ "$last" == *:* ]] && repo="${image%:*}"
	repo="${repo#docker.io/}"
	repo="${repo#index.docker.io/}"
	repo="${repo#library/}"
	echo "other/repo@sha256:0000"
	echo "mirror.example/${repo}@sha256:0000"
	echo "${repo}@sha256:$(printf '%s' "$repo" | sha256sum | cut -c1-12)"
	exit 0
fi
exit 1
EOF

# cosign: logs the verified reference; fails for references in $STUB_BAD,
# and when the required version annotation is not $STUB_SIGNED_VERSION (if
# set).
cat >"$tmp/bin/cosign" <<'EOF'
#!/usr/bin/env bash
ref="${!#}"
echo "cosign $*" >>"$STUB_LOG"
case " $STUB_BAD " in *" $ref "*) echo "Error: no signatures found" >&2; exit 1 ;; esac
if [ -n "$STUB_SIGNED_VERSION" ]; then
	case " $* " in
	*" version=$STUB_SIGNED_VERSION "*) ;;
	*) echo "Error: no matching signatures: missing or incorrect annotation" >&2; exit 1 ;;
	esac
fi
exit 0
EOF
chmod +x "$tmp/bin/docker" "$tmp/bin/cosign"
# A PATH with only what the script needs before looking for cosign, so a
# cosign installed on the host is not found. Wrappers, not copies: the real
# binaries keep finding their libraries next to them.
mkdir -p "$tmp/nocosign"
for tool in bash dirname; do
	printf '#!%s\nexec %s "$@"\n' "$(command -v bash)" "$(command -v "$tool")" >"$tmp/nocosign/$tool"
	chmod +x "$tmp/nocosign/$tool"
done

digest_of() {
	printf '%s' "$1" | sha256sum | cut -c1-12
}

# run <images> [env assignments...]: runs the script, output in $out, exit
# code in $code, calls in $tmp/log.
run() {
	local images="$1"
	shift
	: >"$tmp/log"
	set +e
	out="$(env PATH="$tmp/bin:$PATH" STUB_LOG="$tmp/log" STUB_IMAGES="$images" \
		STUB_BAD="" STUB_NO_DIGEST="" STUB_PULL_FAIL="" STUB_SIGNED_VERSION="" COSIGN_PUBLIC_KEY="$tmp/cosign.pub" \
		"$@" "$SCRIPT" 2>&1)"
	code=$?
	set -e
}

check() {
	if ! eval "$2"; then
		echo "FAIL: $1" >&2
		echo "  output: $out" >&2
		echo "  calls: $(cat "$tmp/log")" >&2
		failed=1
	fi
}

own=ghcr.io/acme/svc:1.2.0
worker=ghcr.io/acme/svc-worker:1.2.0
pinned=postgres:17.11-alpine3.24@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24
ported=registry.local:5000/acme/svc:1.2.0

# Signed images pass, each verified by its pulled digest; pinned ones are
# skipped; the pull happens before any check.
run "$own $worker $pinned"
check "signed images are accepted" '[ "$code" = 0 ]'
check "own image verified by digest" "grep -qF 'ghcr.io/acme/svc@sha256:$(digest_of ghcr.io/acme/svc)' '$tmp/log'"
check "worker image verified by digest" "grep -qF 'ghcr.io/acme/svc-worker@sha256:$(digest_of ghcr.io/acme/svc-worker)' '$tmp/log'"
check "a repository that only contains the name is not used" '! grep -q "cosign.*mirror.example" "$tmp/log"'
check "digest-pinned image is not verified" '! grep -q "cosign.*postgres" "$tmp/log"'
check "own image signature must carry its tag as version" "grep -qF -- '-a version=1.2.0 ghcr.io/acme/svc@sha256:' '$tmp/log'"
check "key and tlog flags are passed" "grep -qF -- '--key $tmp/cosign.pub --insecure-ignore-tlog=true' '$tmp/log'"
check "pull runs before the first check" '[ "$(grep -n "compose.*pull" "$tmp/log" | cut -d: -f1)" -lt "$(grep -n "^cosign" "$tmp/log" | head -1 | cut -d: -f1)" ]'
check "count is reported" 'grep -qF "2 image(s) verified" <<<"$out"'

# A registry with a port keeps its ':' in the repository name.
run "$ported"
check "registry port is kept" "[ \"\$code\" = 0 ] && grep -qF 'registry.local:5000/acme/svc@sha256:$(digest_of registry.local:5000/acme/svc)' '$tmp/log'"

# Docker Hub images are matched by the short name docker records.
run "docker.io/acme/svc:1.2.0 docker.io/library/redis:7.4.0 library/nginx:1.29.0 ghcr.io/library/tool:1.0.0"
check "docker.io prefix is accepted" '[ "$code" = 0 ]'
check "library/ image verified by its short name" "grep -qF ' nginx@sha256:$(digest_of nginx)' '$tmp/log'"
check "library/ under another registry is kept" "grep -qF 'ghcr.io/library/tool@sha256:$(digest_of ghcr.io/library/tool)' '$tmp/log'"
check "docker.io image verified by its short name" "grep -qF 'acme/svc@sha256:$(digest_of acme/svc)' '$tmp/log'"
check "docker.io/library image verified by its short name" "grep -qF 'redis@sha256:$(digest_of redis)' '$tmp/log'"

# A signature for another release does not pass.
run "$own" STUB_SIGNED_VERSION=1.1.0
check "signature for another version is refused" '[ "$code" = 1 ] && grep -qF "do not deploy it" <<<"$out" && grep -qF "incorrect annotation" <<<"$out"'

# An image without a tag has no version to check against.
run "ghcr.io/acme/svc"
check "untagged image is refused" '[ "$code" = 1 ] && grep -qF "no version tag" <<<"$out"'

# An unsigned image stops the deploy and names the image.
bad="ghcr.io/acme/svc-worker@sha256:$(digest_of ghcr.io/acme/svc-worker)"
run "$own $worker" STUB_BAD="$bad"
check "unsigned image is refused" '[ "$code" = 1 ]'
check "refusal names the image" 'grep -qF "$worker" <<<"$out" && grep -qF "do not deploy it" <<<"$out"'
check "cosign error is shown" 'grep -qF "no signatures found" <<<"$out"'

# An image without a registry digest cannot be verified.
run "$own" STUB_NO_DIGEST="$own"
check "image without digest is refused" '[ "$code" = 1 ] && grep -qF "no registry digest" <<<"$out"'

# Without the public key nothing is pulled.
run "$own" COSIGN_PUBLIC_KEY="$tmp/missing.pub"
check "missing key is refused" '[ "$code" = 1 ] && grep -qF "missing.pub not found" <<<"$out"'
check "missing key pulls nothing" '[ ! -s "$tmp/log" ]'

# Without cosign nothing is pulled.
run "$own" PATH="$tmp/nocosign"
check "missing cosign is refused" '[ "$code" = 1 ] && grep -qF "cosign is not installed" <<<"$out"'
check "missing cosign pulls nothing" '[ ! -s "$tmp/log" ]'

# IMAGE_VERIFY=off still pulls but checks nothing, with a warning; other
# values are refused.
run "$own" IMAGE_VERIFY=off
check "IMAGE_VERIFY=off skips the check" '[ "$code" = 0 ] && grep -qF "WARNING" <<<"$out" && ! grep -q "^cosign" "$tmp/log"'
check "IMAGE_VERIFY=off still pulls" 'grep -q "compose.*pull" "$tmp/log"'
run "$own" IMAGE_VERIFY=off STUB_PULL_FAIL=1
check "IMAGE_VERIFY=off deploys local images when the pull fails" '[ "$code" = 0 ] && grep -qF "registry unreachable" <<<"$out" && grep -qF "already on this host" <<<"$out"'
run "$own" STUB_PULL_FAIL=1
check "a failed pull stops the verified deploy" '[ "$code" = 1 ] && ! grep -q "^cosign" "$tmp/log"'
run "$own" IMAGE_VERIFY=maybe
check "unknown IMAGE_VERIFY is refused" '[ "$code" = 1 ] && grep -qF "must be on or off" <<<"$out"'

if [ "$failed" != 0 ]; then
	exit 1
fi
echo "verify-images_test.sh: ok"
