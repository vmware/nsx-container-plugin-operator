# Copyright © 2026 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
FROM alpine:3.19

# Copy entrypoint script
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Create symlinks for all commands called by the operator's manifests
RUN ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/init_k8s_node && \
    ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/check_pod_liveness && \
    ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/start_host_ovs_monitor && \
    ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/start_node_agent && \
    ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/start_kube_proxy && \
    ln -s /usr/local/bin/entrypoint.sh /usr/local/bin/start_ovs

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
