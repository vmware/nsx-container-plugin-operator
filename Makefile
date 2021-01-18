# go options
GO                 ?= go
LDFLAGS            :=
GOFLAGS            :=
BINDIR             ?= $(CURDIR)/build/bin
GO_FILES           := $(shell find . -type d -name '.cache' -prune -o -type f -name '*.go' -print)
GOPATH             ?= $$($(GO) env GOPATH)

.PHONY: all
all: build

include versioning.mk

LDFLAGS += $(VERSION_LDFLAGS)
OPERATOR_NAME = vmware/nsx-container-plugin-operator

.PHONY: build
build:
	GOOS=linux $(GO) build -o $(BINDIR)/$(OPERATOR_NAME) $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/manager
	docker build -f build/Dockerfile . -t $(OPERATOR_NAME):$(DOCKER_IMG_VERSION)

.PHONY: bin
bin:
	GOOS=linux $(GO) build -o $(BINDIR)/$(OPERATOR_NAME) $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/manager

.PHONY: test-unit
test-unit:
	GOOS=linux $(GO) test -race -cover github.com/vmware/nsx-container-plugin-operator/pkg...

.PHONY: clean
clean:
	rm -f $(BINDIR)/$(OPERATOR_NAME)
