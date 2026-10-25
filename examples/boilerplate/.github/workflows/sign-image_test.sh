#!/usr/bin/env bash
# Tests sign-image.sh, which signs what release.yaml publishes, with a
# stand-in for cosign on PATH. Runs in vogel's CI and works as is in a
# generated repository: .github/workflows/sign-image_test.sh
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/sign-image.sh"
failed=0

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/runner"

# cosign: logs its arguments and the key it can read.
cat >"$tmp/bin/cosign" <<'EOF'
#!/usr/bin/env bash
echo "cosign $* key=$COSIGN_PRIVATE_KEY" >>"$STUB_LOG"
EOF
chmod +x "$tmp/bin/cosign"

image=ghcr.io/acme/svc@sha256:0123456789abcdef

# run [env assignments...]: runs the script, output in $out, exit code in
# $code, calls in $tmp/log.
run() {
	: >"$tmp/log"
	set +e
	out="$(env PATH="$tmp/bin:$PATH" STUB_LOG="$tmp/log" RUNNER_TEMP="$tmp/runner" \
		IMAGE="$image" VERSION=1.2.0 COSIGN_PRIVATE_KEY=secret-key COSIGN_PASSWORD=pw \
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

# The digest is signed with the key from the environment, the private signing
# config and the release version.
run
check "signing succeeds" '[ "$code" = 0 ]'
check "signing config is created first" "[ \"\$(head -1 '$tmp/log')\" = 'cosign signing-config create --out $tmp/runner/signing-config.json key=secret-key' ]"
check "digest is signed with version and config" "grep -qxF 'cosign sign --yes --key env://COSIGN_PRIVATE_KEY --signing-config $tmp/runner/signing-config.json -a version=1.2.0 $image key=secret-key' '$tmp/log'"

# Without the key nothing is signed, with a pointer to the README.
run COSIGN_PRIVATE_KEY=
check "missing key is refused" '[ "$code" = 1 ] && grep -qF "COSIGN_PRIVATE_KEY is not set" <<<"$out" && grep -qF "README" <<<"$out"'
check "missing key signs nothing" '[ ! -s "$tmp/log" ]'

# Without an image or a version nothing is signed.
run IMAGE=
check "missing image is refused" '[ "$code" = 1 ] && grep -qF "IMAGE is not set" <<<"$out" && [ ! -s "$tmp/log" ]'
run VERSION=
check "missing version is refused" '[ "$code" = 1 ] && grep -qF "VERSION is not set" <<<"$out" && [ ! -s "$tmp/log" ]'

if [ "$failed" != 0 ]; then
	exit 1
fi
echo "sign-image_test.sh: ok"
