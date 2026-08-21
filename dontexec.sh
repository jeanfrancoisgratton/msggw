#!/usr/bin/env bash
# dontexec.sh -- create or remove the _dontexec marker file
#
# Lives at the root of the repo; every path it touches is relative to that
# root, so the script can be dropped into any repo unchanged.
#
#   dontexec.sh <dir>      create _dontexec in <dir>
#   dontexec.sh -r <dir>   remove _dontexec from <dir>
#   dontexec.sh -g         create _dontexec in the repo root
#   dontexec.sh -gr        remove _dontexec from the repo root

set -euo pipefail

# the repo root is wherever this script sits; work from there so that
# everything below is a plain relative path
cd "$(dirname "$0")"

MARKER="_dontexec"

usage() {
	cat >&2 <<-EOF
	Usage: ${0##*/} [-r] <directory>
	       ${0##*/} -g[r]

	  -r  remove the ${MARKER} file instead of creating it
	  -g  operate on the repo root

	<directory> must be one of the repo root's __* subdirectories:
	$(printf '  %s\n' __*/)
	EOF
	exit 1
}

remove=0
global=0

while getopts ":rg" opt; do
	case "${opt}" in
		r) remove=1 ;;
		g) global=1 ;;
		*) usage ;;
	esac
done
shift $((OPTIND - 1))

if ((global)); then
	(($# == 0)) || usage
	target="."
else
	(($# == 1)) || usage
	target="${1#./}"    # tolerate ./__debian
	target="${target%/}" # tolerate __debian/
	# only the repo root's own __* subdirectories are legal targets: no
	# absolute paths, no traversal, no nesting
	if [[ "${target}" != __* || "${target}" == */* ]]; then
		echo "${0##*/}: ${1}: not a __* subdirectory of the repo root" >&2
		exit 1
	fi
fi

if [[ ! -d "${target}" ]]; then
	echo "${0##*/}: ${target}: no such directory" >&2
	exit 1
fi

[[ "${target}" == "." ]] && label="${MARKER}" || label="${target}/${MARKER}"

if ((remove)); then
	rm -f -- "${target}/${MARKER}"
	echo "removed ${label}"
else
	touch -- "${target}/${MARKER}"
	echo "created ${label}"
fi
