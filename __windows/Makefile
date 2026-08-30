# msggw — Windows (.msi) packaging
#
# Recipes:
#   make build     -> cross-compile msggw.exe and package it into
#                      a real .msi with msitools' wixl
#   make release   -> upload the freshly built .msi to the Nexus winLocal repo
#   make clean     -> remove the staging tree and the built .msi
#   make info      -> print the values extracted from msggw.json
#
# Windows has no distro package manager, so there's no APKBUILD/control/spec/
# PKGBUILD equivalent here. Metadata (name / version / binary / description)
# is read straight from ../msggw.json instead -- the manifest
# stubber itself writes at `create` time -- so this stays a single source of
# truth, same principle as every other __*/Makefile this tool generates.
#
# "Packaging" here means: cross-compile with GOOS=windows GOARCH=amd64
# CGO_ENABLED=0 (built inside the winbuilder container), then build a proper
# .msi installer around it with `wixl` (msitools -- a from-scratch, C,
# Linux-native reimplementation of the WiX toolchain; no Windows/.NET/mono
# needed to produce a real Windows Installer file). msggw.wxs
# is the WiX source; read the comment at its top before touching the
# UpgradeCode or the Component Guid inside it -- both are permanent
# identifiers, not build artifacts. Values wrapped in $(var.X) in that file
# are filled in below via `wixl -D X=value`; wixl substitutes those as plain
# string values, so passing $(DESCRIPTION) unescaped here is correct --
# XML-escaping only matters for text written directly into the .wxs source,
# not for -D values.
#
# Windows Installer has no dpkg/rpm/apk-style release field: ProductVersion is
# the only thing its upgrade logic (the MajorUpgrade rules in the .wxs)
# compares to decide whether a new .msi replaces an already-installed one. A
# packaging-only change -- RELEASE bumped, code untouched -- still has to move
# that number, or a rebuilt .msi is either a silent no-op ("already
# installed") or, worse, a stray side-by-side install, since wixl mints a
# fresh ProductCode every build. MSI also only parses/compares ProductVersion's
# first three numeric fields, so RELEASE below replaces VERSION's own patch
# digit rather than riding along as an ignored fourth field.

SHELL       := /bin/bash
.SHELLFLAGS := -o pipefail -c

MANIFEST     := ../msggw.json

NAME         := $(shell jq -r '.softwarename'  $(MANIFEST))
VERSION      := $(shell jq -r '.versionnumber' $(MANIFEST))
RELEASE      := $(shell jq -r '.releasenumber' $(MANIFEST))
BINARY       := $(shell jq -r '.binaryname'    $(MANIFEST))
DESCRIPTION  := $(shell jq -r '.description'   $(MANIFEST))
MANUFACTURER := $(shell jq -r '.manufacturer'  $(MANIFEST))
MSIVERSION   := $(shell echo $(VERSION) | cut -d. -f1,2).$(RELEASE)

NEXUSREPO := winLocal
SRCDIR    := ../src
ARCH      := amd64
STAGING   := $(NAME)-$(VERSION)-$(RELEASE)_windows_$(ARCH)
MSI       := $(CURDIR)/$(STAGING).msi

# Where the .msi installs to. No per-product subdirectory is appended --
# INSTALLDIR is the literal install path -- so override this here (or with
# `make build INSTALLDIR=...`) if this tool needs its own directory.
INSTALLDIR := C:\utils

.PHONY: build upload release clean info

build:
	rm -rf "$(STAGING)"
	install -d "$(STAGING)"
	cd $(SRCDIR) && CGO_ENABLED=0 GOOS=windows GOARCH=$(ARCH) go build -trimpath \
	  -ldflags="-s -w -buildid=" -o "$(CURDIR)/$(STAGING)/$(BINARY).exe" .
	wixl -D ProductName=$(NAME) -D ProductVersion=$(MSIVERSION) \
	  -D Manufacturer=$(MANUFACTURER) -D "Description=$(DESCRIPTION)" \
	  -D Staging=$(STAGING) -D BinaryName=$(BINARY) -D InstallDir=$(INSTALLDIR) \
	  -o "$(MSI)" msggw.wxs

upload:
	/opt/bin/nxtools upload $(NEXUSREPO) $(MSI)

release:
	${MAKE} build
	${MAKE} upload
	${MAKE} clean

clean:
	rm -rf "$(STAGING)" "$(MSI)"

info:
	@echo "NAME        = $(NAME)"
	@echo "VERSION     = $(VERSION)"
	@echo "RELEASE     = $(RELEASE)"
	@echo "MSIVERSION  = $(MSIVERSION)"
	@echo "BINARY      = $(BINARY)"
	@echo "DESCRIPTION = $(DESCRIPTION)"
	@echo "ARCH        = $(ARCH)"
	@echo "STAGING     = $(STAGING)"
	@echo "MSI         = $(MSI)"
	@echo "INSTALLDIR  = $(INSTALLDIR)"
