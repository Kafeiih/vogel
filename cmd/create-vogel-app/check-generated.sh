#!/usr/bin/env bash
# Generates projects from this checkout's examples/boilerplate with
# create-vogel-app, one per component combination, and checks that each one
# builds, vets (integration tag included), regenerates its OpenAPI server
# code unchanged, passes its unit tests and renders its compose files. A
# component marker placed wrong in the boilerplate breaks one of these
# combinations, so CI runs this on every change.
#
# Usage: cmd/create-vogel-app/check-generated.sh   (from the repository root)
# Needs: go, docker (compose config only, no containers), network for
# `go mod tidy`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

go build -o "$WORK/create-vogel-app" "$ROOT/cmd/create-vogel-app"

# name|flags
combos=(
	"all|"
	"minimal|--without worker --without cerbos --without storage --without tracing"
	"no-worker|--without worker"
	"no-notification|--without notification"
	"no-cerbos|--without cerbos"
	"no-storage|--without storage"
	"no-tracing|--without tracing"
	"bpms|--with bpms"
	"bpms-lean|--with bpms --without notification --without cerbos --without tracing"
)

for combo in "${combos[@]}"; do
	name="svc-${combo%%|*}"
	read -r -a flags <<<"${combo#*|}"
	echo "==> $name ${flags[*]:-}"
	if ! (cd "$WORK" && ./create-vogel-app --from "$ROOT" --yes --owner ci --no-git "${flags[@]}" "$name" >"$WORK/$name.log" 2>&1); then
		cat "$WORK/$name.log" >&2
		exit 1
	fi
	(
		cd "$WORK/$name"
		go build ./...
		go vet ./...
		go vet -tags=integration ./...
		# The committed server code must be what go generate produces from the
		# project's own api/openapi.yaml, with the component markers stripped.
		before="$(find . -type f -exec sha256sum {} + | sort)"
		go generate ./...
		if [ "$before" != "$(find . -type f -exec sha256sum {} + | sort)" ]; then
			echo "FAIL: $name: go generate changed the generated project" >&2
			exit 1
		fi
		if ! go test ./... >"$WORK/$name.test.log" 2>&1; then
			cat "$WORK/$name.test.log" >&2
			exit 1
		fi
		{
			echo "APP_VERSION=0.0.0"
			echo "DB_HOST=postgres"
			echo "DB_NAME=db"
			echo "APP_MIGRATOR_PASSWORD=x"
			echo "APP_RUNTIME_PASSWORD=x"
			echo "CERBOS_HOST=cerbos:3593"
			echo "CERBOS_PLAINTEXT=true"
		} >deploy/.env
		docker compose -f deploy/docker-compose.yaml config -q
		docker compose -f deploy/docker-compose.yaml --profile bundled-db --profile tools config -q
		docker compose -f deploy/docker-compose.developer.yaml config -q
		if grep -rIlE 'vogel:(component [a-z]+|only) (begin|end)' . >/dev/null; then
			echo "FAIL: $name still has marker lines" >&2
			exit 1
		fi
	)
	echo "    ok"
done
echo "check-generated: all combinations OK"
