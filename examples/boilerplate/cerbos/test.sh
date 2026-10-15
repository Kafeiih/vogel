#!/usr/bin/env bash
# Compile the policies and run their tests (make cerbos-test).
#
# Builds the Dockerfile's validate stage instead of running a Cerbos image
# directly, so the Cerbos version comes only from the Dockerfile's pin.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

docker build --target validate -f "${SCRIPT_DIR}/Dockerfile" "${SCRIPT_DIR}"
