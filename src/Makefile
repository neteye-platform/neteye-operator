# Image URL to use all building/pushing image targets.
IMG ?= docker-si.wuerth-phoenix.com/neteye-operator:dev
# Development inputs are exported as raw values. $(value ...) prevents Make from
# evaluating a supplied literal Make function before the shell receives it.
REGISTRY ?= 172.19.69.254:5000
DEV_VERSION ?=
MANAGER_IMG ?=
BUNDLE_IMG ?=
CATALOG_IMG ?=
KEYCLOAK_SOURCE_IMG ?= ghcr.io/neteye-platform/neteye-keycloak:1.0.1
KEYCLOAK_IMG ?=
DEV_CONTEXT ?= bin/dev/context
DEV_IMAGE_PULL_SECRET ?=
KUBECONFIG ?=
override export REGISTRY := $(value REGISTRY)
override export DEV_VERSION := $(value DEV_VERSION)
override export MANAGER_IMG := $(value MANAGER_IMG)
override export BUNDLE_IMG := $(value BUNDLE_IMG)
override export CATALOG_IMG := $(value CATALOG_IMG)
override export KEYCLOAK_SOURCE_IMG := $(value KEYCLOAK_SOURCE_IMG)
override export KEYCLOAK_IMG := $(value KEYCLOAK_IMG)
override export DEV_CONTEXT := $(value DEV_CONTEXT)
override export DEV_IMAGE_PULL_SECRET := $(value DEV_IMAGE_PULL_SECRET)
override export KUBECONFIG := $(value KUBECONFIG)

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

.PHONY: dev-render
dev-render: ## Render an isolated bundle/catalog context for local OLM v1 development.
	@set -eu; \
    registry="$${REGISTRY:-172.19.69.254:5000}"; \
	version="$${DEV_VERSION:-0.1.1-dev.$$(date -u +%Y%m%d%H%M%S).$$(git rev-parse --short HEAD 2>/dev/null || printf nogit)}"; \
	context="$${DEV_CONTEXT:-bin/dev/context}"; \
	keycloak="$${KEYCLOAK_IMG:-$$registry/neteye-keycloak:1.0.1}"; \
	go run ./hack/dev-render --source-root . --output-context "$$context" --registry "$$registry" --version "$$version" --keycloak-image "$$keycloak" --image-pull-secret "$${DEV_IMAGE_PULL_SECRET:-}"

.PHONY: dev-mirror-keycloak
dev-mirror-keycloak: ## Mirror the Keycloak runtime image to the local registry.
	@set -eu; \
    registry="$${REGISTRY:-172.19.69.254:5000}"; \
	source="$${KEYCLOAK_SOURCE_IMG:-ghcr.io/neteye-platform/neteye-keycloak:1.0.1}"; \
	target="$${KEYCLOAK_IMG:-$$registry/neteye-keycloak:1.0.1}"; \
	docker pull "$$source"; docker tag "$$source" "$$target"; docker push "$$target"

.PHONY: dev-publish
dev-publish: ## Build, render, and push local development manager, bundle, and catalog images.
	@set -eu; \
    registry="$${REGISTRY:-172.19.69.254:5000}"; \
	version="$${DEV_VERSION:-0.1.1-dev.$$(date -u +%Y%m%d%H%M%S).$$(git rev-parse --short HEAD 2>/dev/null || printf nogit)}"; \
	context="$${DEV_CONTEXT:-bin/dev/context}"; \
	manager="$${MANAGER_IMG:-$$registry/neteye-operator:$$version}"; \
	bundle="$${BUNDLE_IMG:-$$registry/neteye-operator-bundle:$$version}"; \
	catalog="$${CATALOG_IMG:-$$registry/neteye-operator-catalog:$$version}"; \
	keycloak="$${KEYCLOAK_IMG:-$$registry/neteye-keycloak:1.0.1}"; \
	test "$$manager" = "$$registry/neteye-operator:$$version"; \
	test "$$bundle" = "$$registry/neteye-operator-bundle:$$version"; \
	test "$$catalog" = "$$registry/neteye-operator-catalog:$$version"; \
	docker build -f Dockerfile --build-arg VERSION="$$version" -t "$$manager" .; docker push "$$manager"; \
	go run ./hack/dev-render --source-root . --output-context "$$context" --registry "$$registry" --version "$$version" --keycloak-image "$$keycloak" --image-pull-secret "$${DEV_IMAGE_PULL_SECRET:-}"; \
	docker build -f "$$PWD/Dockerfile.bundle" -t "$$bundle" "$$context"; docker push "$$bundle"; \
	docker build -f "$$PWD/Dockerfile.catalog" -t "$$catalog" "$$context"; docker push "$$catalog"

.PHONY: dev-install
dev-install: ## Install the rendered version through OLM v1 using the existing operatorhubio catalog.
	@set -eu; \
	kubeconfig="$${KUBECONFIG:?set KUBECONFIG to the VM cluster kubeconfig}"; \
	context="$${DEV_CONTEXT:-bin/dev/context}"; \
	version_file="$$context/.dev-version"; \
	catalog_file="$$context/.catalog-image"; \
	test -s "$$version_file" || { printf '%s\n' "missing rendered state: $$version_file; run make dev-publish first" >&2; exit 1; }; \
	test -s "$$catalog_file" || { printf '%s\n' "missing rendered state: $$catalog_file; run make dev-publish first" >&2; exit 1; }; \
	version="$$(tr -d '\n' < "$$version_file")"; \
	catalog_image="$$(tr -d '\n' < "$$catalog_file")"; \
	go run ./hack/dev-render --validate-version "$$version"; \
	go run ./hack/dev-render --validate-image "$$catalog_image"; \
	KUBECONFIG="$$kubeconfig" kubectl get clustercatalog operatorhubio; \
	KUBECONFIG="$$kubeconfig" helm upgrade --install neteye-operator ../charts --namespace neteye-system --create-namespace --set-string "operator.catalogImage=$$catalog_image" --set-string "operator.versionRange==$$version" --set-string "operator.upgradeConstraintPolicy=SelfCertified"

.PHONY: dev-clean
dev-clean: ## Remove the temporary local development render context.
	@set -eu; context="$${DEV_CONTEXT:-bin/dev/context}"; go run ./hack/dev-render --source-root . --clean-output "$$context"

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
