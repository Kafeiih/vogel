#!/usr/bin/env bash
# Tests code-changed.sh. The CI workflow runs it before trusting the script to
# skip the required checks on a documentation-only pull request.
#
# Usage: .github/scripts/code-changed_test.sh
set -euo pipefail

script="$(dirname "${BASH_SOURCE[0]}")/code-changed.sh"
failures=0

check() {
  local want="$1" name="$2" input="$3" got
  got="$(printf '%s' "$input" | "$script")"
  if [ "$got" != "$want" ]; then
    echo "FAIL: $name: got $got, want $want"
    failures=$((failures + 1))
  fi
}

check false "root Markdown" $'README.md\nCHANGELOG.md\n'
check false "nested Markdown" $'examples/boilerplate/deploy/RUNBOOK.md\n'
check false "docs tree" $'docs/WIRING.md\ndocs/security/diagram.svg\n'
check false ".atl tree" $'.atl/skill-registry.json\n'
check true "Go file" $'logger/logger.go\n'
check true "docs plus code" $'docs/WIRING.md\nworker/river.go\n'
check true "workflow file" $'.github/workflows/ci.yaml\n'
check true "Markdown-like name that is not .md" $'docs.go\nREADME.md.tmpl\n'
check true "docs prefix outside docs/" $'docsite/index.html\n'
check true "code file moved into docs/, both paths listed" $'logger/logger.go\ndocs/logger.go\n'
check false "last line without newline" 'README.md'
check true "last code line without newline" $'README.md\nmain.go'
check true "empty diff" ''
check true "blank lines only" $'\n\n'

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "code-changed.sh: OK"
