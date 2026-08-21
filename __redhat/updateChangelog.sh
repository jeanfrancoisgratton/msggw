#!/usr/bin/env bash

# Prepend a %changelog entry to a specfile.
#
#   updateChangelog.sh              update this checkout's specfile in place
#   updateChangelog.sh --apply FILE insert the same entry into FILE instead
#
# The entry is always computed from this checkout; --apply only changes where
# it lands. The release flow builds main but records the changelog on develop,
# so the duplicate guard follows the target file -- guarding this checkout's
# copy instead is how the entry ends up recorded twice.
#
# Notices go to stderr; nothing here writes to stdout.

set -euo pipefail

SPEC="$(dirname "$0")/msggw.spec"
TARGET="${SPEC}"

note() { echo "$*" >&2; }
die()  { echo "ERROR: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply)
      [[ $# -ge 2 ]] || die "--apply requires a specfile path"
      TARGET="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '3,7p' "$0" >&2
      exit 0
      ;;
    *)
      die "unknown argument '$1'"
      ;;
  esac
done

[[ -f "$TARGET" ]] || die "'$TARGET' does not exist"

DATE=$(date +"%a %b %d %Y")
VERSION=$(rpmspec -q --qf '%{version}\n' "$SPEC" | head -1)
REL=$(rpmspec -q --qf '%{release}\n' "$SPEC" | head -1)
MAINTAINER="Binary package builder <builder@famillegratton.net>"

LAST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || git rev-list --max-parents=0 HEAD)

# If HEAD itself is already tagged (e.g. the release was tagged before being
# built), fall back to the previous tag so LAST_TAG..HEAD is not empty.
if [[ "$(git rev-list -n1 "$LAST_TAG")" == "$(git rev-parse HEAD)" ]]; then
  LAST_TAG=$(git describe --tags --abbrev=0 "${LAST_TAG}^" 2>/dev/null || git rev-list --max-parents=0 HEAD)
fi

ENTRIES=$(git log --oneline "${LAST_TAG}..HEAD" | sed 's/^[a-f0-9]\+ /- /')

if [[ -z "$ENTRIES" ]]; then
  note "No new commits since last tag, nothing to append."
  exit 0
fi

# Guard against duplicate entries
if grep -qF "${VERSION}-${REL}" "$TARGET"; then
  note "Changelog entry for ${VERSION}-${REL} already present in ${TARGET}, skipping."
  exit 0
fi

# Without a %changelog line awk rewrites the file unchanged and calls it
# success; --apply hands us a file we did not write, so check.
grep -q '^%changelog$' "$TARGET" || die "'$TARGET' has no %changelog section"

NEW_ENTRY="* ${DATE} ${MAINTAINER} ${VERSION}-${REL}"$'\n'"${ENTRIES}"

awk -v entry="$NEW_ENTRY" '
  /^%changelog$/ { print; print entry; print ""; next }
  { print }
' "$TARGET" > "$TARGET.tmp" && mv "$TARGET.tmp" "$TARGET"

note "Changelog updated for ${VERSION}-${REL} in ${TARGET}"
