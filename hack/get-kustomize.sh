#!/usr/bin/env bash

# Copyright © 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
KUSTOMIZE_VERSION="v3.9.1"
_BINDIR=$THIS_DIR/.bin

# Check if you have kustomize installed, or download it.
# please note that different versions of kustomize can give different results
check_or_install_kustomize() {
    # Check if there is already a kustomize binary in $_BINDIR and if yes, check
    # if the version matches the expected one.
    local kustomize="$(PATH=$_BINDIR command -v kustomize)"
    if [ -x "$kustomize" ]; then
        local kustomize_version="$($kustomize version --short)"
        # Should work with following styles:
        #  - kustomize/v3.3.0
        #  - {kustomize/v3.8.2  2020-08-29T17:44:01Z  }
        kustomize_version="${kustomize_version##*/}"
        kustomize_version="${kustomize_version%% *}"
        if [ "${kustomize_version}" == "${KUSTOMIZE_VERSION}" ]; then
            # If version is exact match, stop here.
            >&2 echo "Found "$kustomize" version "$kustomize_version
            echo "$kustomize"
            return 0
        fi
        # If we are here kustomize version isn't the right one
        >&2 echo "Found "$kustomize" version "$kustomize_version", expecting "$KUSTOMIZE_VERSION". Installing desired version"
    fi
    local kustomize_url="https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz"
    curl -sLo kustomize.tar.gz "${kustomize_url}" || return 1
    mkdir -p $_BINDIR || return 1
    tar -xzf kustomize.tar.gz -C "$_BINDIR" || return 1
    kustomize=$_BINDIR/kustomize
    rm -f kustomize.tar.gz
    echo $kustomize
    return 0
}
