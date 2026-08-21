#!/usr/bin/env bash
set -euo pipefail

# One-time provisioning of the RPMBUILDER container. This installs the specfile's
# BuildRequires into the running container; they persist there. rpmbuild only
# *checks* BuildRequires, it never installs (nor removes) anything itself.

echo "Installing BuildRequires dependencies"; echo

sudo dnf install -y dnf-plugins-core rpm-build rpmdevtools

# Reads BuildRequires straight from the specfile, so there is a single source of
# truth and versioned constraints are honoured.
sudo dnf builddep -y msggw.spec

echo; echo; echo "Done. Now installing the Go binaries"

# This script runs from __redhat/, so go.version is one level up. Reading it as
# a bare 'go.version' left VER empty and fetched a nonexistent tarball, which is
# why a Go version bump appeared to be ignored.
VER=$(cat ../go.version)
ARCH=${1:-amd64}

echo "Fetching archive..."
sudo wget -q "https://go.dev/dl/go${VER}.linux-${ARCH}.tar.gz" -O /opt/go.tar.gz

echo "Unarchiving..."
cd /opt && sudo rm -rf go && sudo tar zxf go.tar.gz && sudo rm -f go.tar.gz

echo "Completed."
