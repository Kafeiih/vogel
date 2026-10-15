#!/usr/bin/env bash
# Smoke-test a built PDP image: prove it SERVES its policies, not only that
# they compiled (make cerbos-smoke).
#
# The validate stage proves the policies compile and pass their tests. It
# does not prove the final image answers correctly: the final stage copies
# .cerbos.yaml, which nothing validates. A config pointing storage elsewhere
# yields an image that starts and reports healthy while denying every
# request, because an empty policy store denies everything. The ALLOW checks
# below are what catch that; the DENY checks catch a rule widened by mistake.
#
# Usage: ./cerbos/smoke.sh <image-ref>
#        ./cerbos/smoke.sh               # builds the image first
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for dep in docker curl jq; do
	command -v "$dep" >/dev/null 2>&1 || {
		echo "smoke.sh needs '$dep' on the host: the PDP image has no shell to run checks inside." >&2
		exit 1
	}
done

IMAGE="${1:-}"
if [[ -z "$IMAGE" ]]; then
	IMAGE="boilerplate-cerbos:smoke"
	echo "==> building $IMAGE"
	docker build -t "$IMAGE" "$SCRIPT_DIR"
fi

CONTAINER="cerbos-smoke-$$"
cleanup() {
	docker rm -f -v "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Port 0 lets the daemon pick a free port, so concurrent runs do not collide.
echo "==> starting $IMAGE"
docker run -d --name "$CONTAINER" -p 127.0.0.1:0:3592 "$IMAGE" >/dev/null
if ! ADDR="$(docker port "$CONTAINER" 3592/tcp | head -n1)" || [[ -z "$ADDR" ]]; then
	echo "FAIL: the PDP container did not stay up" >&2
	docker logs "$CONTAINER" >&2 || true
	exit 1
fi
BASE="http://${ADDR}"

echo "==> waiting for the PDP at ${BASE}"
healthy=0
for _ in $(seq 1 30); do
	if curl -fsS "${BASE}/_cerbos/health" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	sleep 1
done
if ((!healthy)); then
	echo "FAIL: the PDP never became healthy" >&2
	docker logs "$CONTAINER" >&2 || true
	exit 1
fi

# check <description> <role> <resource-id> <action> <expected-effect>
check() {
	local desc="$1" role="$2" id="$3" action="$4" want="$5" got body
	body=$(jq -n --arg role "$role" --arg id "$id" --arg action "$action" '{
		requestId: "smoke",
		principal: {id: ("smoke-" + $role), roles: [$role]},
		resources: [{resource: {kind: "audit:entry", id: $id}, actions: [$action]}]
	}')
	got=$(
		curl -fsS -X POST "${BASE}/api/check/resources" \
			-H 'Content-Type: application/json' -d "$body" |
			jq -r --arg action "$action" '.results[0].actions[$action] // "NO_RESULT"'
	)
	if [[ "$got" != "$want" ]]; then
		echo "FAIL: ${desc}: expected ${want}, got ${got}" >&2
		return 1
	fi
	echo "ok: ${desc} (${got})"
}

# Mirrors policies/resource_audit_entry.yaml (list: admin; view: admin and
# auditor). Keep in sync when that policy changes.
failed=0
check "admin can list audit entries" admin "*" list EFFECT_ALLOW || failed=1
check "admin can view an audit entry" admin entry-1 view EFFECT_ALLOW || failed=1
check "auditor can view an audit entry" auditor entry-1 view EFFECT_ALLOW || failed=1
check "auditor cannot list audit entries" auditor "*" list EFFECT_DENY || failed=1
check "other roles cannot list audit entries" user "*" list EFFECT_DENY || failed=1
check "other roles cannot view an audit entry" user entry-1 view EFFECT_DENY || failed=1

if ((failed)); then
	echo "==> PDP logs:" >&2
	docker logs "$CONTAINER" >&2 || true
	echo "SMOKE TEST FAILED" >&2
	exit 1
fi

echo "SMOKE TEST PASSED: the image serves its policies"
