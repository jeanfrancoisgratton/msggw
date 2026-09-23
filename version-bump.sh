#!/usr/bin/env sh
# msggw — bump the software version everywhere it needs to be typed by
# hand, and reset every distro's independent release counter to 1.
#
# Version and release are different things (see docs/CHANGELOG.md history /
# the packaging Makefiles): release numbers are NOT kept in sync across
# distros day to day — a Debian-only packaging fix bumps only
# __debian/control, not the RPM spec. But a new *version* is a fresh package
# on every distro, so every release counter resets to 1 here. If one distro
# needs to diverge afterward (e.g. a distro-specific repackaging issue),
# edit that one file's release field by hand — this script only handles
# version bumps.
#
# src/cmd/root.go needs no edit: it reads its version via -ldflags -X at
# build time from whichever file below is authoritative for that build.
# __windows and __macos need no edit either: __windows/Makefile reads
# msggw.json straight via jq, and __macos/build-macos.sh reads it via sed,
# same as src/build.sh — so bumping msggw.json alone is enough for both.

set -eu

SCRIPT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"

TARGETS="msggw.json __alpine/APKBUILD __archlinux/PKGBUILD __debian/control __redhat/msggw.spec"

print_usage() {
  cat <<EOF
Usage: $0 <new-version>   (e.g. $0 1.3.2)

Bumps the software version in msggw.json and every __*/ packaging
file, and resets each distro's independent release counter to 1. The
version must start with a digit and contain only [A-Za-z0-9._~-] (no "v"
prefix, no flags).
EOF
}

die() {
  echo "ERROR: $1" >&2
  exit "${2:-1}"
}

# --- argument handling -------------------------------------------------

case "${1-}" in
  -h | --help)
    print_usage
    exit 0
    ;;
esac

[ "$#" -eq 1 ] || { print_usage >&2; exit 2; }
NEWVERSION="$1"

case "$NEWVERSION" in
  -*)
    die "'$NEWVERSION' looks like an option, not a version. See '$0 --help'." 2
    ;;
  [0-9]*) ;;
  *)
    die "version must start with a digit (e.g. 1.3.2), got '$NEWVERSION'"
    ;;
esac

case "$NEWVERSION" in
  *[!A-Za-z0-9._~-]*)
    die "'$NEWVERSION' contains characters not expected in a version string"
    ;;
esac

# --- preconditions -------------------------------------------------------

for f in $TARGETS; do
  [ -f "$f" ] || die "expected to find $f here — is this script still at the project root?"
done

# Refuse to run against a dirty tree for the files we're about to rewrite,
# so any mistake here (or in a future edit to this script) is always a
# trivial 'git checkout -- <file>' away from being undone.
dirty=$(git status --porcelain -- $TARGETS 2>/dev/null || true)
if [ -n "$dirty" ]; then
  echo "ERROR: the following files have uncommitted changes; commit or stash them first" >&2
  echo "so this bump stays trivially revertible:" >&2
  echo "$dirty" >&2
  exit 1
fi

# --- patch ----------------------------------------------------------------

# patch <file> <sed-script>  — writes via a temp file + mv so this works
# identically under GNU, BSD, and BusyBox sed (their -i flags don't agree),
# then restores the original file's permissions (mv onto a freshly created
# temp file would otherwise silently drop the executable bit).
patch() {
  file="$1"
  script="$2"
  mode=$(stat -c '%a' "$file" 2>/dev/null || stat -f '%Lp' "$file")
  sed "$script" "$file" > "$file.bumptmp"
  mv "$file.bumptmp" "$file"
  chmod "$mode" "$file"
}

# verify <file> <expected-literal-line>  — fails loudly instead of silently
# no-op'ing if a file's format ever drifts out from under this script's sed
# patterns.
verify() {
  grep -qF -- "$2" "$1" || die "expected to find '$2' in $1 after patching — its format may have changed; check 'git diff' before re-running."
}

echo "Bumping msggw to version $NEWVERSION (release reset to 1 on every distro)"

patch msggw.json \
  "s/\"versionnumber\": *\"[^\"]*\"/\"versionnumber\": \"$NEWVERSION\"/
   s/\"defaultreleasenumber\": *\"[^\"]*\"/\"defaultreleasenumber\": \"1\"/"
verify msggw.json "\"versionnumber\": \"$NEWVERSION\""
verify msggw.json "\"defaultreleasenumber\": \"1\""

patch __alpine/APKBUILD \
  "s/^pkgver=.*/pkgver=$NEWVERSION/
   s/^pkgrel=.*/pkgrel=1/"
verify __alpine/APKBUILD "pkgver=$NEWVERSION"
verify __alpine/APKBUILD "pkgrel=1"

patch __archlinux/PKGBUILD \
  "s/^pkgver=.*/pkgver=$NEWVERSION/
   s/^pkgrel=.*/pkgrel=1/"
verify __archlinux/PKGBUILD "pkgver=$NEWVERSION"
verify __archlinux/PKGBUILD "pkgrel=1"

patch __redhat/msggw.spec \
  "s/^%define _version .*/%define _version $NEWVERSION/
   s/^%define _rel .*/%define _rel 1/"
verify __redhat/msggw.spec "%define _version $NEWVERSION"
verify __redhat/msggw.spec "%define _rel 1"

patch __debian/control \
  "s/^Version: .*/Version: $NEWVERSION-1/"
verify __debian/control "Version: $NEWVERSION-1"

echo
echo "Changed:"
grep -H '"versionnumber"\|"defaultreleasenumber"' msggw.json
grep -H '^pkgver=\|^pkgrel=' __alpine/APKBUILD
grep -H '^pkgver=\|^pkgrel=' __archlinux/PKGBUILD
grep -H '^Version:' __debian/control
grep -H '^%define _version\|^%define _rel' __redhat/msggw.spec
echo
echo "Nothing else to edit by hand — src/cmd/root.go picks this up via -ldflags at build time,"
echo "and __windows/Makefile + __macos/build-macos.sh both read msggw.json directly."
