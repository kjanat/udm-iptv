#!/bin/sh
set -eu
source_dir=$(CDPATH='' cd -- "${1:?usage: build.sh pinned-IMProxy-source-directory}" && pwd)
patch_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
patch --directory "${source_dir}" -p1 --forward --fuzz=0 \
	<"${patch_dir}/0001-ipv4-querier-election.patch"
bash "${patch_dir}/test.sh" "${source_dir}"
make -C "${source_dir}" -B "CC=${CC:-cc}" "LDFLAGS=${LDFLAGS:--static}"
