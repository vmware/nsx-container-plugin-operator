FROM golang:1.21.5 as golang-build
FROM registry.access.redhat.com/ubi8/ubi:latest

LABEL name="nsx-container-plugin-operator"
LABEL maintainer="NSX Containers Team <container-dev.pdl@broadcom.com>"
LABEL summary="A cluster operator to deploy nsx-ncp CNI plugin"
LABEL version="0.0.3"
LABEL release="1"
LABEL description="Manage deployments, daemonsets, and config maps for NSX Integration"
LABEL vendor="VMware"

COPY LICENSE /licenses/

ENV OPERATOR=/usr/local/bin/nsx-ncp-operator \
    USER_UID=1001 \
    USER_NAME=nsx-ncp-operator

COPY build/bin /usr/local/bin
COPY manifest /manifest
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
