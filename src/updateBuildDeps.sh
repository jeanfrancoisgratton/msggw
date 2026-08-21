#!/usr/bin/env bash
set -euo pipefail

# Upgrade every module explicitly listed in require(...) or require x y
go mod edit -json \
  | jq -r '.Require[].Path' \
  | while IFS= read -r mod; do
      [ -n "$mod" ] || continue
      echo "updating $mod"
      go get "${mod}@latest"
    done

go mod tidy