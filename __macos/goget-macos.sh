#!/usr/bin/env bash

# Fetches a Go toolchain for macOS and installs it under /opt/go-versions,
# then symlinks /opt/go to it -- the same layout the other __*/ builders use
# (see e.g. __redhat/rpmbuild-deps.sh), so PATH=$PATH:/opt/go/bin works the
# same way everywhere.
#
# Usage:
#   ./goget-macos.sh [version] [arch]
#
# Arguments:
#   $1 = Go version (optional) -- defaults to ../go.version, the same file
#        every other builder reads, so this can't silently drift from what
#        the rest of the project builds with. Pass one explicitly to
#        override it (e.g. to test an upcoming Go release).
#   $2 = macOS architecture (optional) -- defaults to this Mac's own
#        (`uname -m`, mapped to Go's arm64/amd64), so it does the right
#        thing on both Apple Silicon and Intel without having to ask.
#
# Examples:
#   ./goget-macos.sh                # version from ../go.version, this Mac's arch
#   ./goget-macos.sh 1.27.0          # explicit version, this Mac's arch
#   ./goget-macos.sh 1.27.0 amd64    # explicit version, force Intel build

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VER="${1:-}"
if [ -z "$VER" ]; then
    VERFILE="${SCRIPT_DIR}/../go.version"
    [ -f "$VERFILE" ] || { echo "No version given and $VERFILE not found." >&2; exit 3; }
    VER="$(tr -d '[:space:]' < "$VERFILE")"
fi

ARCH="${2:-}"
if [ -z "$ARCH" ]; then
    case "$(uname -m)" in
        arm64)  ARCH="arm64" ;;
        x86_64) ARCH="amd64" ;;
        *)      echo "Unrecognized architecture: $(uname -m) -- pass it explicitly as \$2." >&2; exit 3 ;;
    esac
fi

OS="darwin"
GOVERSIONS_DIR="/opt/go-versions"
TARBALL="/tmp/go${VER}.${OS}-${ARCH}.tar.gz"

echo "Using version: $VER"
echo "Using OS:      $OS"
echo "Using arch:    $ARCH"

sudo mkdir -p "$GOVERSIONS_DIR"

echo "Fetching archive..."
# macOS ships curl, not wget, out of the box.
curl -fsSL "https://go.dev/dl/go${VER}.${OS}-${ARCH}.tar.gz" -o "$TARBALL" || { echo "download failed" >&2; exit 1; }

echo "Unarchiving..."
sudo rm -rf "${GOVERSIONS_DIR:?}/${VER}"
sudo tar zxf "$TARBALL" -C "$GOVERSIONS_DIR"
sudo mv "${GOVERSIONS_DIR}/go" "${GOVERSIONS_DIR}/${VER}"
rm -f "$TARBALL"

sudo rm -rf /opt/go
sudo ln -s "${GOVERSIONS_DIR}/${VER}" /opt/go

echo "Completed. Add /opt/go/bin to PATH if it isn't already:"
echo "  export PATH=\"/opt/go/bin:\$PATH\""
