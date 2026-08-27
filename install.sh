#!/bin/sh
# Installation script for the udm-iptv service
#
# Copyright (C) 2022 Fabian Mastenbroek.
#
# This program is free software; you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation; either version 2 of the License, or
# (at your option) any later version.

set -e

if command -v unifi-os > /dev/null 2>&1; then
    echo "error: You need to be in UniFi OS to run the installer."
    echo "Please run the following command to enter UniFi OS:"
    echo
    printf "\t unifi-os shell\n"
    exit 1
fi

UDM_IPTV_VERSION="${UDM_IPTV_VERSION:-3.0.6}"
UDM_IPTV_REPOSITORY="${UDM_IPTV_REPOSITORY:-fabianishere/udm-iptv}"
UDM_IPTV_STATE_DIR="${UDM_IPTV_STATE_DIR:-/data/udm-iptv}"
UDM_IPTV_RUN="${UDM_IPTV_RUN:-}"
UDM_IPTV_PR="${UDM_IPTV_PR:-}"
UDM_IPTV_TOKEN="${UDM_IPTV_TOKEN:-${GITHUB_TOKEN:-}}"
UDM_IPTV_TIMEOUT_SECONDS="${UDM_IPTV_TIMEOUT_SECONDS:-900}"

case "${UDM_IPTV_PR}" in
    */*\#*)
        UDM_IPTV_REPOSITORY="${UDM_IPTV_PR%\#*}"
        UDM_IPTV_PR="${UDM_IPTV_PR##*\#}"
        ;;
    *) ;;
esac

api="https://api.github.com/repos/${UDM_IPTV_REPOSITORY}"

# Every caller treats an empty answer as a failure, so a failed request yields
# nothing instead of aborting the script.
api_get() {
    if [ -n "${UDM_IPTV_TOKEN}" ]; then
        curl -fsSL -H "Authorization: Bearer ${UDM_IPTV_TOKEN}" "$@" || true
    else
        curl -fsSL "$@" || true
    fi
}

json_field() {
    sed -n "s/.*\"$1\": *\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p" | head -n 1
}

resolve_head() {
    api_get "${api}/pulls/${UDM_IPTV_PR}" | json_field sha
}

resolve_run() {
    if [ -n "${UDM_IPTV_PR}" ]; then
        api_get "${api}/actions/runs?head_sha=$1&per_page=1" | json_field id
    elif [ "${UDM_IPTV_RUN}" = "latest" ]; then
        api_get "${api}/actions/runs?status=success&per_page=1" | json_field id
    else
        echo "${UDM_IPTV_RUN}"
    fi
}

await_run() {
    waited=0
    while [ "${waited}" -lt "${UDM_IPTV_TIMEOUT_SECONDS}" ]; do
        status=$(api_get "${api}/actions/runs/$1" | json_field status)
        case "${status}" in
            completed) return 0 ;;
            "")
                echo "error: Could not read the status of run $1."
                return 1
                ;;
            *) ;;
        esac
        echo "Waiting for run $1 to complete (${status})..."
        sleep 15
        waited=$((waited + 15))
    done
    echo "error: Run $1 did not complete within ${UDM_IPTV_TIMEOUT_SECONDS}s."
    return 1
}

dest=$(mktemp -d)

if [ -n "${UDM_IPTV_RUN}" ] || [ -n "${UDM_IPTV_PR}" ]; then
    if [ -n "${UDM_IPTV_PR}" ]; then
        head=$(resolve_head)
        [ -n "${head}" ] || {
            echo "error: Pull request ${UDM_IPTV_PR} not found in ${UDM_IPTV_REPOSITORY}."
            exit 1
        }
        echo "Pull request ${UDM_IPTV_PR} is at ${head}."
        run=$(resolve_run "${head}")
    else
        run=$(resolve_run)
    fi

    [ -n "${run}" ] || {
        echo "error: No workflow run found in ${UDM_IPTV_REPOSITORY}."
        exit 1
    }

    await_run "${run}"

    conclusion=$(api_get "${api}/actions/runs/${run}" | json_field conclusion)
    [ "${conclusion}" = "success" ] || {
        echo "error: Run ${run} concluded ${conclusion}."
        exit 1
    }

    zip=$(api_get "${api}/actions/runs/${run}/artifacts" | json_field archive_download_url)
    [ -n "${zip}" ] || {
        echo "error: Run ${run} has no artifacts."
        exit 1
    }

    if [ -z "${UDM_IPTV_TOKEN}" ]; then
        echo "error: Downloading a workflow artifact requires a token."
        echo "Set UDM_IPTV_TOKEN to a personal access token with 'actions:read',"
        echo "or install from a release instead."
        exit 1
    fi

    echo "Downloading artifact of run ${run}..."
    curl -fsSL -H "Authorization: Bearer ${UDM_IPTV_TOKEN}" -o "${dest}/artifact.zip" "${zip}"
    busybox unzip -o -q -d "${dest}" "${dest}/artifact.zip"

    deb=$(find "${dest}" -name '*.deb' | head -n 1)
    [ -n "${deb}" ] || {
        echo "error: No package found in the artifact of run ${run}."
        exit 1
    }
    mv "${deb}" "${dest}/udm-iptv.deb"
else
    UDM_IPTV_PACKAGE="${UDM_IPTV_PACKAGE:-https://github.com/${UDM_IPTV_REPOSITORY}/releases/download/v${UDM_IPTV_VERSION}/udm-iptv_${UDM_IPTV_VERSION}_all.deb}"

    case "${UDM_IPTV_PACKAGE}" in
        http://*|https://*)
            echo "Downloading ${UDM_IPTV_PACKAGE}..."
            curl -fsSL -o "${dest}/udm-iptv.deb" "${UDM_IPTV_PACKAGE}"
            ;;
        *)
            echo "Using ${UDM_IPTV_PACKAGE}..."
            cp "${UDM_IPTV_PACKAGE}" "${dest}/udm-iptv.deb"
            ;;
    esac
fi

# Fix permissions on the packages
chown _apt:root "${dest}/udm-iptv.deb"

echo "Installing packages..."

# Update APT sources (best effort)
apt-get update >/dev/null || true

# Install dialog package for interactive install
apt-get install -q -y dialog >/dev/null || echo "Failed to install dialog... Using readline frontend"

# Install udm-iptv
apt-get install -o Acquire::AllowUnsizedPackages=1 -q "${dest}/udm-iptv.deb"

# Keep the package next to the saved answers so that a firmware update can be
# recovered from without network access
if [ -d "$(dirname "${UDM_IPTV_STATE_DIR}")" ]; then
    mkdir -p "${UDM_IPTV_STATE_DIR}"
    cp "${dest}/udm-iptv.deb" "${UDM_IPTV_STATE_DIR}/udm-iptv.deb"
fi

# Delete downloaded packages
rm -rf "${dest}"

echo "Installation successful... You can find your configuration at /etc/udm-iptv.conf."
echo
echo "Use the following command to reconfigure the script:"
echo
printf "\t udm-iptv reconfigure\n"
