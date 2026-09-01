# msggw — Debian (.deb) packaging
#
# Recipes:
#   make build     -> build the Go binary and assemble the .deb
#   make release   -> upload the freshly built .deb to the Nexus aptLocal repo (with reindex)
#   make clean     -> remove the staging tree and the built .deb
#   make info      -> print the values extracted from the control file
#
# Package metadata (name / version / arch) is read straight from the debian
# control file so there is a single source of truth. This build recipe folds
# together what 2.build_binary.sh + restore_repo.sh used to do: it copies
# (never moves) the DEBIAN control files into a throwaway staging tree, so
# there is nothing to git-restore afterwards.

SHELL       := /bin/bash
.SHELLFLAGS := -o pipefail -c

CONTROL   := control

NAME      := $(shell awk -F': +' '/^Package:/{print $$2}'      $(CONTROL))
VERSION   := $(shell awk -F': +' '/^Version:/{print $$2}'      $(CONTROL))
ARCH      := $(shell awk -F': +' '/^Architecture:/{print $$2}' $(CONTROL))
BINARY    := msg-gw

NEXUSREPO := aptLocal
SRCDIR    := ../src
STAGING   := $(NAME)-$(VERSION)_$(ARCH)
DEB       := $(CURDIR)/$(STAGING).deb

.PHONY: build release clean info

build:
	rm -rf "$(STAGING)"
	install -d "$(STAGING)/DEBIAN" "$(STAGING)/opt/sbin" "$(STAGING)/usr/share/doc/$(NAME)"
	cp control preinst prerm postinst postrm "$(STAGING)/DEBIAN/"
	# copyright is documentation, not a control file: policy puts it at
	# /usr/share/doc/<package>/copyright, and it must be 0644 there.
	install -m 0644 copyright "$(STAGING)/usr/share/doc/$(NAME)/copyright"
	cd $(SRCDIR) && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" \
	  -o "$(CURDIR)/$(STAGING)/opt/sbin/$(BINARY)" .
	sudo chown -R 0:0 "$(STAGING)/opt/sbin/$(BINARY)"
	dpkg-deb -b "$(STAGING)" "$(DEB)"

upload:
	/opt/bin/nxtools upload -r $(NEXUSREPO) $(DEB)


clean:
	rm -rf "$(STAGING)" "$(DEB)"

release:
	${MAKE} build
	${MAKE} upload
	${MAKE} clean

info:
	@echo "NAME    = $(NAME)"
	@echo "VERSION = $(VERSION)"
	@echo "ARCH    = $(ARCH)"
	@echo "BINARY  = $(BINARY)"
	@echo "STAGING = $(STAGING)"
	@echo "DEB     = $(DEB)"
