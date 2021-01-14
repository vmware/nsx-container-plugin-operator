#!/usr/bin/env bash

# Copyright © 2021 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
KUSTOMIZE_VERSION="v3.9.1"

# Check if you have kustomize installed, or download it.
# please note that different versions of kustomize can give different results
check_or_install_kustomize() {
    # Check if there is already a kustomize binary in $_BINDIR and if yes, check
    # if the version matches the expected one.
    local kustomize="$(PATH=$THIS_DIR command -v kustomize)"
    if [ -x "$kustomize" ]; then
        >&2 echo "Found "$kustomize" version "`$kustomize version --short`
        echo $kustomize
        return 0
    fi
    local kustomize_url="https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize/${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz"
    curl -sLo kustomize.tar.gz "${kustomize_url}" || return 1
    tar -xzf kustomize.tar.gz || return 1
    rm -f kustomize.tar.gz
    echo $kustomize
    return 0
}
