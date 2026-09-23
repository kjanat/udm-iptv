#!/bin/bash
set -eu
ulimit -c 0
source_dir=$(CDPATH='' cd -- "${1:?usage: test.sh IMProxy-source-directory}" && pwd)
tests_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
build_dir=$(mktemp -d)
trap 'rm -rf -- "${build_dir}"' EXIT HUP INT TERM
for source in "${source_dir}"/src/*.c; do
	name=$(basename "${source}" .c)
	if [[ "${name}" = proxy ]]; then
		"${CC:-cc}" -Wall -Wextra -I"${source_dir}/include" -Dmain=improxy_program_main \
			-c "${source}" -o "${build_dir}/${name}.o"
	else
		"${CC:-cc}" -Wall -Wextra -I"${source_dir}/include" \
			-c "${source}" -o "${build_dir}/${name}.o"
	fi
done
"${CC:-cc}" -Wall -Wextra -Werror -I"${source_dir}/include" \
	"${tests_dir}/test_querier.c" "${build_dir}"/*.o \
	-Wl,--wrap=get_sysuptime -Wl,--wrap=recvmsg \
	-Wl,--wrap=send_igmp_mld_query -Wl,--wrap=k_mcast_join \
	-Wl,--wrap=k_mcast_leave -Wl,--wrap=imp_membership_db_update \
	-o "${build_dir}/test-querier"
"${build_dir}/test-querier"
