#!/usr/bin/env sh

set -eu

BINARY=message-gateway
OUTPUT="/opt/sbin"
DRY_RUN=false

usage() {
  echo "Usage: $0 [-b|--binary NAME] [--dry-run] [OUTPUT_DIR]" >&2
  exit 2
}

cleanup_dryrun() {
  # Only delete if we created it
  if [ "${DRYRUN_OUTPATH-}" ] && [ -f "$DRYRUN_OUTPATH" ]; then
    rm -f -- "$DRYRUN_OUTPATH"
  fi
}
trap cleanup_dryrun EXIT INT TERM HUP

# Parse arguments
while [ "$#" -gt 0 ]; do
  case "$1" in
    -b|--binary)
      shift
      [ "${1-}" ] || usage
      BINARY="$1"
      ;;
    --dry-run|--dry_run)
      DRY_RUN=true
      ;;
    --)
      shift
      break
      ;;
    -*)
      usage
      ;;
    *)
      OUTPUT="$1"
      ;;
  esac
  shift
done

# This script lives in __macos/, but the Go module is in ../src -- resolve
# both relative to the script's own location, not the caller's cwd, so it
# works whether it's run as ./build-macos.sh, from another directory, or via
# a symlink into PATH.
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
SRC_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/../src" && pwd)"

# Determine branch name against the repo, not whatever the caller's cwd
# happens to be; handle detached HEAD cleanly.
BRANCH="$(git -C "$SRC_DIR" symbolic-ref -q --short HEAD 2>/dev/null || echo "detached_$(git -C "$SRC_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)")"

# Sanitize for filenames:
# - replace / with _
# - map any other odd chars to _
BRANCH="$(printf '%s' "$BRANCH" | tr '/' '_' | tr -c 'A-Za-z0-9._-' '_')"

if [ "$BRANCH" = "master" ] || [ "$BRANCH" = "main" ] || [ "$BRANCH" = "develop" ]; then
  FULLNAME="$BINARY"
else
  FULLNAME="${BINARY}-${BRANCH}"
fi

# Resolve OUTPUT to an absolute path *before* changing directory below, so a
# relative OUTPUT_DIR (or the default) is honoured against the caller's own
# cwd, not against $SRC_DIR.
mkdir -p "$OUTPUT"
OUTPUT="$(CDPATH= cd -- "$OUTPUT" && pwd)"

OUTPATH="${OUTPUT%/}/$FULLNAME"
if [ "$DRY_RUN" = "true" ]; then
  DRYRUN_OUTPATH="${OUTPATH}.DRYRUN"
  echo "Dry-run build: $DRYRUN_OUTPATH (will be deleted)"
  BUILD_OUTPATH="$DRYRUN_OUTPATH"
else
  echo "Building $OUTPATH"
  BUILD_OUTPATH="$OUTPATH"
fi

cd "$SRC_DIR"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o "$BUILD_OUTPATH" .
