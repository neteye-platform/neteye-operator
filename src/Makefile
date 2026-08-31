# Image URL to use all building/pushing image targets.
IMG ?= docker-si.wuerth-phoenix.com/neteye-operator:dev

# Tool versions.
CONTROLLER_GEN_VERSION ?= v0.21.0
VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null || echo dev)
LD_FLAGS ?= -X main.version=$(VERSION)

# Local tool installation directory. Keeping generated tooling under the module
# makes builds reproducible and avoids depending on globally installed binaries.
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
GOLANGCI_LINT ?= golangci-lint
ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_VERSION ?= release-0.24
ENVTEST_K8S_VERSION ?= 1.36.2
GENERATED_CRD ?= bundle/manifests/neteye.cloud_neteyes.yaml
NETEYE_CRD ?= bundle/manifests/neteyes.neteye.cloud.crd.yaml

.PHONY: all
all: build

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: controller-gen ## Generate CRD manifests into the OLM bundle manifests directory.
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths="./api/..." output:crd:artifacts:config=bundle/manifests
	mv $(GENERATED_CRD) $(NETEYE_CRD)

.PHONY: verify-generated
verify-generated: generate manifests ## Regenerate code/manifests and fail if generated files are not committed.
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go $(NETEYE_CRD)

.PHONY: fmt
fmt: ## Run gofmt against Go code.
	gofmt -w main.go api/v1alpha1/*.go
	gofmt -w controllers/*.go internal/keycloak/*.go

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: ## Run golangci-lint and apply available fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: test
test: generate fmt vet manifests envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./...

.PHONY: build
build: generate fmt vet ## Build the manager binary.
	go build -ldflags "$(LD_FLAGS)" -o bin/manager main.go

.PHONY: docker-build
docker-build: ## Build the operator container image.
	docker build -f Dockerfile --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: clean
clean: ## Remove local build outputs.
	rm -rf bin

.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if needed.
	@test -s $(CONTROLLER_GEN) && $(CONTROLLER_GEN) --version | grep -q $(CONTROLLER_GEN_VERSION) || \
		{ mkdir -p $(LOCALBIN); GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION); }

$(CONTROLLER_GEN):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Install setup-envtest (downloads apiserver/etcd on first test run).

$(ENVTEST):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
