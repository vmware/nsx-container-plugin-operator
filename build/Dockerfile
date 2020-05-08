FROM golang:1.13 AS builder

WORKDIR /nsx-ncp-operator
COPY . /nsx-ncp-operator
RUN go build -o /nsx-ncp-operator/build/bin/nsx-ncp-operator ./cmd/manager

FROM registry.access.redhat.com/ubi8/ubi:latest

LABEL maintainer="NSX Containers Team <nsx-container-dev@vmware.com>"
LABEL description="A cluster operator to deploy nsx-ncp CNI plugin"

ENV OPERATOR=/usr/local/bin/nsx-ncp-operator \
    USER_UID=1001 \
    USER_NAME=nsx-ncp-operator

# install operator binary
COPY --from=builder /nsx-ncp-operator/build/bin/nsx-ncp-operator ${OPERATOR}

COPY build/bin /usr/local/bin
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
