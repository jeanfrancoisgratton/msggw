SPEC      := msggw.spec
NAME      := $(shell rpmspec -q --qf '%{name}\n'    $(SPEC) | head -1)
VERSION   := $(shell rpmspec -q --qf '%{version}\n' $(SPEC) | head -1)
RELEASE   := $(shell rpmspec -q --qf '%{release}\n' $(SPEC) | head -1)
SOURCES   := $(HOME)/rpmbuild/SOURCES
RPMS      := $(HOME)/rpmbuild/RPMS
DEST      := /data
REPOROOT  := $(shell git rev-parse --show-toplevel)
NEXUSREPO := dnfLocal
# RPM uses '~' as its pre-release separator (0.30.00~DEBUG sorts *before*
# 0.30.00), but git forbids '~' in a ref name. Swapping it for '-' yields a
# valid tag that is also a valid SemVer pre-release, so it keeps the same
# "sorts before the final release" meaning: v0.30.00-DEBUG.
# Only the tag is rewritten -- the tarball keeps the raw RPM version, since
# Source0 in the specfile expects $(NAME)-$(VERSION).tar.gz verbatim.
TAG       := v$(subst ~,-,$(VERSION))
# Branch the changelog commit is recorded on -- not the branch being built.
CL_BRANCH ?= develop
# Specfile path relative to the repository root, to address it inside another
# branch's tree. Pathspecs resolve against the current directory, which is why
# commitcl needs --full-tree.
SPECREL   := $(shell git ls-files --full-name -- $(SPEC))

.PHONY: tag release tarball check-tags changelog rpm rpmcl upload commitcl clean

# Intended workflow: make release

tarball:
	git -C $(REPOROOT) archive --prefix=$(NAME)-$(VERSION)/ HEAD \
	  | gzip > $(SOURCES)/$(NAME)-$(VERSION).tar.gz

check-tags:
	@# 0. Fail loudly here rather than deep inside `make release`:
	@git check-ref-format "refs/tags/$(TAG)" || { \
	  echo "ERROR: '$(TAG)' is not a valid git tag name (derived from version '$(VERSION)')."; \
	  exit 1; \
	}
	@# 1. The condition that silently breaks changelog generation:
	@if git describe --exact-match --tags HEAD >/dev/null 2>&1; then \
	  echo "WARNING: HEAD is already tagged $$(git describe --exact-match --tags HEAD)."; \
	  echo "         updateChangelog.sh will fall back to the previous tag for the range."; \
	fi
	@# 2. Local/remote tag agreement:
	@local=$$(git rev-parse -q --verify "refs/tags/$(TAG)" 2>/dev/null); \
	 remote=$$(git ls-remote --tags origin "refs/tags/$(TAG)" 2>/dev/null | cut -f1); \
	 if   [ -n "$$local" ] && [ -z "$$remote" ]; then echo "NOTE: $(TAG) exists locally but is not pushed."; \
	 elif [ -z "$$local" ] && [ -n "$$remote" ]; then echo "NOTE: $(TAG) is on origin but not local (run: git fetch --tags)."; \
	 elif [ -n "$$local" ] && [ "$$local" != "$$remote" ]; then \
	   echo "ERROR: $(TAG) points to different commits locally ($$local) and on origin ($$remote)."; \
	   exit 1; \
	 fi

changelog: check-tags tarball
	$(REPOROOT)/__redhat/updateChangelog.sh

rpm: tarball
	rpmbuild -bb $(SPEC)
	find $(RPMS) -name '*.rpm' -exec cp {} $(DEST)/ \;

rpmcl: tarball changelog
	rpmbuild -bb $(SPEC)
	find $(RPMS) -name '*.rpm' -exec cp {} $(DEST)/ \;

upload:
	/opt/bin/nxtools upload -r $(NEXUSREPO) $(DEST)/$(NAME)-$(VERSION)-$(RELEASE).x86_64.rpm

# Record the changelog on $(CL_BRANCH), not on the branch that was built: a
# push to main retriggers the whole build through the Forgejo hook, a push to
# develop is dropped at its branch filter.
#
# Assembled with plumbing because the workspace has to stay on main mid-build,
# and only the new %changelog block crosses over -- copying this branch's
# specfile wholesale would revert whatever develop has bumped since. The
# duplicate guard therefore runs against develop's copy, not ours.
#
# The parent is the tip just fetched, so the push is always a fast-forward; if
# $(CL_BRANCH) moves inside that window it is rejected and the build goes red.
# Every step is idempotent, so re-running is the fix.
commitcl:
	@set -eu; \
	git fetch -q origin $(CL_BRANCH) \
	  || { echo "ERROR: cannot fetch origin/$(CL_BRANCH)" >&2; exit 1; }; \
	base=$$(git rev-parse FETCH_HEAD); \
	meta=$$(git ls-tree --full-tree "$$base" -- "$(SPECREL)"); \
	[ -n "$$meta" ] \
	  || { echo "ERROR: $(SPECREL) does not exist on $(CL_BRANCH)" >&2; exit 1; }; \
	mode=$$(echo "$$meta" | awk '{print $$1}'); \
	oldblob=$$(echo "$$meta" | awk '{print $$3}'); \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	git cat-file blob "$$oldblob" > "$$tmp/spec"; \
	$(REPOROOT)/__redhat/updateChangelog.sh --apply "$$tmp/spec"; \
	newblob=$$(git hash-object -w "$$tmp/spec"); \
	if [ "$$newblob" = "$$oldblob" ]; then \
	  echo "NOTE: $(CL_BRANCH) already carries the changelog for $(VERSION)-$(RELEASE), nothing to commit."; \
	  exit 0; \
	fi; \
	export GIT_INDEX_FILE="$$tmp/index"; \
	git read-tree "$$base"; \
	git update-index --add --cacheinfo "$$mode,$$newblob,$(SPECREL)"; \
	tree=$$(git write-tree); \
	commit=$$(git commit-tree "$$tree" -p "$$base" \
	  -m "chore: update changelog for $(VERSION)-$(RELEASE)"); \
	git push origin "$$commit:refs/heads/$(CL_BRANCH)"; \
	echo "NOTE: changelog for $(VERSION)-$(RELEASE) pushed to $(CL_BRANCH) as $${commit}."

clean:
	rm -f  $(SOURCES)/$(NAME)-$(VERSION).tar.gz
	rm -rf $(HOME)/rpmbuild/BUILD/$(NAME)-$(VERSION)

tag:
	@if git rev-parse -q --verify "refs/tags/$(TAG)" >/dev/null 2>&1; then \
	  echo "Tag $(TAG) already exists locally; not re-creating."; \
	else \
	  git tag -a $(TAG) -m '$(TAG)'; \
	fi
	@if [ -z "$$(git ls-remote --tags origin "refs/tags/$(TAG)" 2>/dev/null)" ]; then \
	  git push origin $(TAG); \
	else \
	  echo "Tag $(TAG) already on origin; nothing to push."; \
	fi

# No `git push`: commitcl pushes its own commit straight to $(CL_BRANCH), and
# nothing else here commits to the built branch.
release:
	${MAKE} rpmcl
	${MAKE} commitcl
	${MAKE} upload
	${MAKE} tag