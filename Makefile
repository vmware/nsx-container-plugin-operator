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
OPERATOR_NAME = nsx-ncp-operator
OPERATOR_IMG_NAME = vmware/nsx-container-plugin-operator

.PHONY: build
build:
	docker build --build-arg LDFLAGS="$(LDFLAGS)" -f build/Dockerfile . -t $(OPERATOR_IMG_NAME):$(DOCKER_IMG_VERSION)
	docker tag $(OPERATOR_IMG_NAME):$(DOCKER_IMG_VERSION) $(OPERATOR_IMG_NAME)

.PHONY: bin
bin:
	GOOS=linux $(GO) build -o $(BINDIR)/$(OPERATOR_NAME) $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/manager

.PHONY: bundle
bundle:
	./bundle/generate_bundle.sh --bundle-repo $(BUNDLE_REPO) --bundle-image $(BUNDLE_IMG_NAME) --bundle-version $(BUNDLE_VERSION)

.PHONY: test-unit
test-unit:
	GOOS=linux $(GO) test -race -cover github.com/vmware/nsx-container-plugin-operator/pkg...

.PHONY: test-unit-simple
test-unit-simple:
	$(GO) test github.com/vmware/nsx-container-plugin-operator/pkg...

.PHONY: clean
clean:
	rm -f $(BINDIR)/$(OPERATOR_NAME)

# --- E2E Testing Targets ---
KIND_CLUSTER_NAME = nsx-operator-test
KIND_NODE_VERSION ?= v1.32.0

ifdef REGISTRY_MIRROR
KIND_NODE_IMAGE ?= $(REGISTRY_MIRROR)/kindest/node:$(KIND_NODE_VERSION)
else
KIND_NODE_IMAGE ?= kindest/node:$(KIND_NODE_VERSION)
endif

.PHONY: kind-start
kind-start:
	kind create cluster --name $(KIND_CLUSTER_NAME) --image $(KIND_NODE_IMAGE) --config deploy/e2e/kind-config.yaml

.PHONY: kind-stop
kind-stop:
	kind delete cluster --name $(KIND_CLUSTER_NAME)

.PHONY: build-test-images
build-test-images:
	@bash -c 'set -e; \
	  MANIFEST=manifest/kubernetes/ubuntu/ncp-ubuntu.yaml; \
	  trap "git checkout $$MANIFEST" EXIT; \
	  python3 -c "import sys; f=\"$$MANIFEST\"; c=open(f).read().replace(\"      annotations:\n        container.apparmor.security.beta.kubernetes.io/nsx-node-agent: localhost/node-agent-apparmor\", \"      annotations: {}\"); open(f,\"w\").write(c)"; \
	  docker build --build-arg LDFLAGS="$(LDFLAGS)" -f build/Dockerfile . -t $(OPERATOR_IMG_NAME):latest'
	docker build -f build/fake-ncp/Dockerfile build/fake-ncp -t vmware/fake-ncp:v1
	docker tag vmware/fake-ncp:v1 vmware/fake-ncp:v2-patched
	docker build -f cmd/nsx-mock/Dockerfile . -t vmware/nsx-mock:latest

.PHONY: load-images
load-images:
	kind load docker-image $(OPERATOR_IMG_NAME):latest --name $(KIND_CLUSTER_NAME)
	kind load docker-image vmware/fake-ncp:v1 --name $(KIND_CLUSTER_NAME)
	kind load docker-image vmware/fake-ncp:v2-patched --name $(KIND_CLUSTER_NAME)
	kind load docker-image vmware/nsx-mock:latest --name $(KIND_CLUSTER_NAME)

.PHONY: deploy-e2e
deploy-e2e:
	kubectl apply -f deploy/kubernetes/operator.nsx.vmware.com_ncpinstalls_crd.yaml
	kubectl apply -f deploy/kubernetes/namespace.yaml
	kubectl create namespace nsx-system --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f deploy/kubernetes/service_account.yaml
	kubectl apply -f deploy/kubernetes/role.yaml
	kubectl apply -f deploy/kubernetes/role_binding.yaml
	kubectl apply -f deploy/e2e/nsx-mock.yaml
	kubectl wait --for=condition=ready pod -l app=nsx-mock -n nsx-system --timeout=2m
	# Create AppArmor host paths on the Kind node to avoid CreateContainerConfigError
	docker exec $(KIND_CLUSTER_NAME)-control-plane mkdir -p /var/cache/apparmor /var/lib/snapd/apparmor/snap-confine
	kubectl apply -f deploy/e2e/configmap.yaml
	kubectl apply -f deploy/e2e/operator.yaml
	kubectl apply -f deploy/kubernetes/operator.nsx.vmware.com_v1_ncpinstall_cr.yaml

.PHONY: test-e2e-local
test-e2e-local:
	$(GO) test -v -count=1 -timeout 15m -ldflags '$(LDFLAGS)' ./test/e2e/...

.PHONY: test-e2e
test-e2e: kind-start build-test-images load-images deploy-e2e test-e2e-local kind-stop

