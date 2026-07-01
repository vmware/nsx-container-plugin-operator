# Copyright © 2026 VMware, Inc. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# Build stage
FROM golang:1.26.4-alpine AS builder
WORKDIR /workspace

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/nsx-mock/main.go cmd/nsx-mock/main.go

# Compile binary
RUN CGO_ENABLED=0 GOOS=linux go build -o nsx-mock ./cmd/nsx-mock

# Runtime stage
FROM alpine:3.19
WORKDIR /
COPY --from=builder /workspace/nsx-mock /usr/local/bin/nsx-mock
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/nsx-mock"]
