#!/usr/bin/env bash
# Reads changed paths, one per line, on stdin and prints "true" when any of
# them can affect the build or tests, "false" when every path is
# documentation: a Markdown file anywhere, or anything under docs/ or .atl/.
# These are the paths the CI workflow's push trigger ignores.
#
# An empty list prints "true": no paths means the diff could not be read, and
# CI must then run every check rather than skip them.
#
# Usage: git diff --name-only BASE...HEAD | .github/scripts/code-changed.sh
set -euo pipefail

seen=false
while IFS= read -r path; do
  [ -n "$path" ] || continue
  seen=true
  case "$path" in
    *.md | docs/* | .atl/*) ;;
    *)
      echo true
      exit 0
      ;;
  esac
done

if [ "$seen" = true ]; then
  echo false
else
  echo true
fi
