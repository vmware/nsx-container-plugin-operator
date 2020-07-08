# go options
GO                 ?= go
LDFLAGS            :=
GOFLAGS            :=
BINDIR             ?= $(CURDIR)/build/bin
GO_FILES           := $(shell find . -type d -name '.cache' -prune -o -type f -name '*.go' -print)
GOPATH             ?= $$($(GO) env GOPATH)

.PHONY: all
all: build

LDFLAGS += $(VERSION_LDFLAGS)
OPERATOR_NAME = nsx-ncp-operator

.PHONY: build
build:
	GOOS=linux $(GO) build -o $(BINDIR)/$(OPERATOR_NAME) $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/manager
	docker build -f build/Dockerfile . -t $(OPERATOR_NAME)

.PHONY: bin
bin:
	GOOS=linux $(GO) build -o $(BINDIR)/$(OPERATOR_NAME) $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/manager

.PHONY: clean
clean:
	rm -f $(BINDIR)/$(OPERATOR_NAME)
