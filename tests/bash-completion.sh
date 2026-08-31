#!/usr/bin/env bash

set -euo pipefail

# shellcheck source=completions/udm-iptv
source completions/udm-iptv

complete_udm_iptv() {
	COMP_WORDS=("$@")
	COMP_CWORD=$((${#COMP_WORDS[@]} - 1))
	COMPREPLY=()
	_udm_iptv
	printf '%s\n' "${COMPREPLY[@]}"
}

assert_contains() {
	local output=$1
	local expected=$2

	grep -Fqx -- "${expected}" <<<"${output}" || {
		printf 'expected completion %q in:\n%s\n' "${expected}" "${output}" >&2
		exit 1
	}
}

assert_excludes() {
	local output=$1
	local unexpected=$2

	if grep -Fqx -- "${unexpected}" <<<"${output}"; then
		printf 'unexpected completion %q in:\n%s\n' "${unexpected}" "${output}" >&2
		exit 1
	fi
}

root=$(complete_udm_iptv udm-iptv '')
assert_contains "${root}" configure
assert_contains "${root}" reconfigure
assert_contains "${root}" upgrade
assert_contains "${root}" diagnose
assert_contains "${root}" --help

upgrade=$(complete_udm_iptv udm-iptv upgrade '')
assert_contains "${upgrade}" --latest
assert_contains "${upgrade}" --version
assert_contains "${upgrade}" --package
assert_contains "${upgrade}" --pr
assert_contains "${upgrade}" --run
assert_contains "${upgrade}" --force
assert_contains "${upgrade}" --repository
assert_contains "${upgrade}" --branch
assert_contains "${upgrade}" --token-file
assert_contains "${upgrade}" --timeout

version=$(complete_udm_iptv udm-iptv upgrade --version 4.1.2 '')
assert_excludes "${version}" --latest
assert_excludes "${version}" --package
assert_excludes "${version}" --pr
assert_excludes "${version}" --run
assert_contains "${version}" --force

run=$(complete_udm_iptv udm-iptv upgrade --run l)
assert_contains "${run}" latest

package=$(complete_udm_iptv udm-iptv upgrade --package udm-i)
assert_contains "${package}" udm-iptv

token_file=$(complete_udm_iptv udm-iptv upgrade --token-file tests/bash-)
assert_contains "${token_file}" tests/bash-completion.sh

uninstall=$(complete_udm_iptv udm-iptv uninstall '')
assert_contains "${uninstall}" --purge
assert_contains "${uninstall}" --keep-data

keep_data=$(complete_udm_iptv udm-iptv uninstall --keep-data '')
assert_excludes "${keep_data}" --purge
assert_excludes "${keep_data}" --keep-data

after_separator=$(complete_udm_iptv udm-iptv upgrade -- '')
[[ -z ${after_separator} ]] || {
	printf 'expected no completions after --, got:\n%s\n' "${after_separator}" >&2
	exit 1
}

complete -p udm-iptv | grep -Fq 'complete -F _udm_iptv udm-iptv'
