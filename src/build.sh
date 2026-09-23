#!/usr/bin/env sh

set -eu

BINARY=msg-gw
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

# Determine branch name; handle detached HEAD cleanly
BRANCH="$(git symbolic-ref -q --short HEAD 2>/dev/null || echo "detached_$(git rev-parse --short HEAD 2>/dev/null || echo unknown)")"

# Sanitize for filenames:
# - replace / with _
# - map any other odd chars to _
BRANCH="$(printf '%s' "$BRANCH" | tr '/' '_' | tr -c 'A-Za-z0-9._-' '_')"

if [ "$BRANCH" = "master" ] || [ "$BRANCH" = "main" ] || [ "$BRANCH" = "develop" ]; then
  FULLNAME="$BINARY"
else
  FULLNAME="${BINARY}-${BRANCH}"
fi

mkdir -p "$OUTPUT"

OUTPATH="${OUTPUT%/}/$FULLNAME"
if [ "$DRY_RUN" = "true" ]; then
  DRYRUN_OUTPATH="${OUTPATH}.DRYRUN"
  echo "Dry-run build: $DRYRUN_OUTPATH (will be deleted)"
  BUILD_OUTPATH="$DRYRUN_OUTPATH"
else
  echo "Building $OUTPATH"
  BUILD_OUTPATH="$OUTPATH"
fi

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
MSGGWVERSION="$(sed -n 's/.*"versionnumber": *"\([^"]*\)".*/\1/p' "$SCRIPT_DIR/../msggw.json")"
BUILDDATE="$(date +%Y.%m.%d)"

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X msggw/cmd.buildVersion=$MSGGWVERSION -X msggw/cmd.buildDate=$BUILDDATE" -o "$BUILD_OUTPATH" .
