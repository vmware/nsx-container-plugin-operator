#!/bin/sh
# Copyright © 2026 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
# Entrypoint for the fake NCP image used in E2E testing

cmd=$(basename "$0")

# If called as entrypoint.sh directly, use the first argument if present
if [ "$cmd" = "entrypoint.sh" ] && [ $# -gt 0 ]; then
    cmd="$1"
    shift
fi

echo "Fake NCP container starting with command: $cmd (args: $@)"

case "$cmd" in
    init_k8s_node)
        echo "Initializing fake K8s node (noop)..."
        exit 0
        ;;
    check_pod_liveness)
        echo "Fake liveness check for $1 succeeded."
        exit 0
        ;;
    start_host_ovs_monitor|start_node_agent|start_kube_proxy|start_ovs|nsx-ncp|entrypoint.sh|*)
        echo "Running fake service $cmd indefinitely..."
        trap 'echo "Received SIGTERM, exiting..."; exit 0' SIGTERM SIGINT
        while true; do
            sleep 3600 &
            wait $!
        done
        ;;
esac
