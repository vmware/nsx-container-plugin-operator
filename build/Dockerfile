FROM golang:1.26.9 AS golang-build

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG LDFLAGS=""
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "${LDFLAGS}" -o build/bin/nsx-ncp-operator ./cmd/manager

# The operator is a static binary, so the runtime image only needs a shell,
# coreutils and the CA bundle. Assemble that rootfs here and copy it onto
# ubi-micro, which keeps the package manager (rpm, microdnf) and its
# dependency chain (curl, libxml2, glib2, ...) out of the final image.
# coreutils-single is requested explicitly: the default "coreutils" pulls in
# openssl-libs, while ubi-micro already ships coreutils-single.
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest AS rootfs

RUN mkdir -p /mnt/rootfs && \
    microdnf install -y --installroot /mnt/rootfs --releasever 9 \
        --config /etc/dnf/dnf.conf --noplugins \
        --setopt=cachedir=/var/cache/microdnf-rootfs \
        --setopt=reposdir=/etc/yum.repos.d \
        --setopt=varsdir=/etc/dnf/vars \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs \
        coreutils-single ca-certificates && \
    rm -rf /mnt/rootfs/var/cache/* /mnt/rootfs/var/log/* /var/cache/microdnf-rootfs

FROM registry.access.redhat.com/ubi9/ubi-micro:latest

LABEL name="nsx-container-plugin-operator"
LABEL maintainer="NSX Containers Team <container-dev.pdl@broadcom.com>"
LABEL summary="A cluster operator to deploy nsx-ncp CNI plugin"
LABEL version="0.0.3"
LABEL release="1"
LABEL description="Manage deployments, daemonsets, and config maps for NSX Integration"
LABEL vendor="VMware"

COPY --from=rootfs /mnt/rootfs/ /

COPY LICENSE /licenses/

ENV OPERATOR=/usr/local/bin/nsx-ncp-operator \
    USER_UID=1001 \
    USER_NAME=nsx-ncp-operator

COPY build/bin/entrypoint /usr/local/bin/entrypoint
COPY build/bin/user_setup /usr/local/bin/user_setup
COPY --from=golang-build /workspace/build/bin/nsx-ncp-operator /usr/local/bin/nsx-ncp-operator
COPY manifest /manifest
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
