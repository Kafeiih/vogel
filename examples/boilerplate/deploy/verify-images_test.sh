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
# logged, `image inspect` answers <repo>@sha256:<digest of the repo> unless
# the image is in $STUB_NO_DIGEST.
cat >"$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$STUB_LOG"
if [ "$1" = compose ]; then
	case "$*" in
	*"config --images"*) printf '%s\n' $STUB_IMAGES ;;
	esac
	exit 0
fi
if [ "$1 $2" = "image inspect" ]; then
	image="${!#}"
	case " $STUB_NO_DIGEST " in *" $image "*) exit 0 ;; esac
	repo="$image"
	last="${image##*/}"
	[[ "$last" == *:* ]] && repo="${image%:*}"
	echo "other/repo@sha256:0000"
	echo "${repo}@sha256:$(printf '%s' "$repo" | sha256sum | cut -c1-12)"
	exit 0
fi
exit 1
EOF

# cosign: logs the verified reference; fails for references in $STUB_BAD.
cat >"$tmp/bin/cosign" <<'EOF'
#!/usr/bin/env bash
ref="${!#}"
echo "cosign $*" >>"$STUB_LOG"
case " $STUB_BAD " in *" $ref "*) echo "Error: no signatures found" >&2; exit 1 ;; esac
exit 0
EOF
chmod +x "$tmp/bin/docker" "$tmp/bin/cosign"

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
		STUB_BAD="" STUB_NO_DIGEST="" COSIGN_PUBLIC_KEY="$tmp/cosign.pub" \
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
check "digest-pinned image is not verified" '! grep -q "cosign.*postgres" "$tmp/log"'
check "key and tlog flags are passed" "grep -qF -- '--key $tmp/cosign.pub --insecure-ignore-tlog=true' '$tmp/log'"
check "pull runs before the first check" '[ "$(grep -n "compose.*pull" "$tmp/log" | cut -d: -f1)" -lt "$(grep -n "^cosign" "$tmp/log" | head -1 | cut -d: -f1)" ]'
check "count is reported" 'grep -qF "2 image(s) verified" <<<"$out"'

# A registry with a port keeps its ':' in the repository name.
run "$ported"
check "registry port is kept" "[ \"\$code\" = 0 ] && grep -qF 'registry.local:5000/acme/svc@sha256:$(digest_of registry.local:5000/acme/svc)' '$tmp/log'"

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

# IMAGE_VERIFY=off skips everything with a warning; other values are refused.
run "$own" IMAGE_VERIFY=off
check "IMAGE_VERIFY=off skips the check" '[ "$code" = 0 ] && grep -qF "WARNING" <<<"$out" && [ ! -s "$tmp/log" ]'
run "$own" IMAGE_VERIFY=maybe
check "unknown IMAGE_VERIFY is refused" '[ "$code" = 1 ] && grep -qF "must be on or off" <<<"$out"'

if [ "$failed" != 0 ]; then
	exit 1
fi
echo "verify-images_test.sh: ok"
