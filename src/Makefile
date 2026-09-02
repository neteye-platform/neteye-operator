# Image URL to use all building/pushing image targets.
IMG ?= ghcr.io/neteye-platform/neteye-operator:$(VERSION)
BUNDLE_IMG ?= ghcr.io/neteye-platform/neteye-operator-bundle:$(VERSION)

# Tool versions.
CONTROLLER_GEN_VERSION ?= v0.21.0
GOLANGCI_LINT_VERSION ?= v2.13.2
KUSTOMIZE_VERSION ?= v5.7.1
OPERATOR_SDK_VERSION ?= v1.42.3
VERSION ?= 0.1.0-alpha3
PACKAGE_NAME ?= neteye-operator
LD_FLAGS ?= -X main.version=$(VERSION)

# Local tool installation directory. Keeping generated tooling under the module
# makes builds reproducible and avoids depending on globally installed binaries.
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
KUSTOMIZE ?= $(LOCALBIN)/kustomize
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_VERSION ?= release-0.24
ENVTEST_K8S_VERSION ?= 1.36.2
CRD_BASES ?= config/crd/bases

.PHONY: all
all: build

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: controller-gen ## Generate CRD manifests from the API into config/crd/bases.
	$(CONTROLLER_GEN) crd:crdVersions=v1 paths="./api/..." output:crd:artifacts:config=$(CRD_BASES)

.PHONY: kustomize-manifests
kustomize-manifests: manifests kustomize operator-sdk ## Generate the Operator SDK CSV base and apply bundle source patches.
	$(OPERATOR_SDK) generate kustomize manifests -q --interactive=false --package $(PACKAGE_NAME)

.PHONY: verify-generated
verify-generated: generate bundle bundle-validate ## Regenerate all code and OLM output, then validate the result.
	git diff --exit-code

.PHONY: fmt
fmt: ## Run gofmt against Go code.
	gofmt -w main.go api/v1alpha1/*.go
	gofmt -w controllers/*.go internal/keycloak/*.go internal/elasticstack/*.go internal/resources/*.go

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint and apply available fixes.
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

.PHONY: bundle
bundle: kustomize-manifests ## Render the versioned OLM bundle directly from the release overlay source.
	@release_dir="$$(mktemp -d "$$(pwd)/config/.bundle-release.XXXXXX")"; \
	trap 'rm -rf "$$release_dir"' EXIT; \
	cp config/release/kustomization.yaml "$$release_dir/kustomization.yaml"; \
	printf '%s\n' '- op: replace' '  path: /metadata/name' '  value: neteye-operator.v$(VERSION)' '- op: replace' '  path: /spec/version' '  value: $(VERSION)' > "$$release_dir/release.yaml"; \
	cd "$$release_dir" && $(KUSTOMIZE) edit set image controller=$(IMG); \
	rm -f "$(CURDIR)/bundle/manifests/"*.yaml; \
	$(KUSTOMIZE) build --load-restrictor LoadRestrictionsNone "$$release_dir" > "$$release_dir/rendered.yaml"; \
	cp "$(CURDIR)/config/crd/bases/neteye.cloud_neteyes.yaml" "$(CURDIR)/bundle/manifests/neteyes.neteye.cloud.crd.yaml"; \
	awk '/^apiVersion: operators.coreos.com\/v1alpha1$$/{csv=1} csv {print}' "$$release_dir/rendered.yaml" | grep -v '^---$$' > "$(CURDIR)/bundle/manifests/neteye-operator.v$(VERSION).clusterserviceversion.yaml"; \
	awk '/^kind: ClusterServiceVersion$$/{csv=1} csv && /^[[:space:]]*deployments:$$/{deployments=1} csv && deployments && /^[[:space:]]*- name: neteye-operator$$/{found=1} END {exit !found}' "$(CURDIR)/bundle/manifests/neteye-operator.v$(VERSION).clusterserviceversion.yaml" || { echo 'rendered CSV is missing deployment neteye-operator' >&2; exit 1; }; \
	VERSION='$(VERSION)' PACKAGE_NAME='$(PACKAGE_NAME)' envsubst '$$VERSION $$PACKAGE_NAME' < "$(CURDIR)/config/bundle/annotations.yaml.tmpl" > "$(CURDIR)/bundle/metadata/annotations.yaml"

.PHONY: bundle-validate
bundle-validate: operator-sdk ## Validate the generated on-disk OLM bundle.
	$(OPERATOR_SDK) bundle validate ./bundle --select-optional name=good-practices --select-optional name=alpha-deprecated-apis

.PHONY: bundle-build
bundle-build: bundle ## Build the OLM bundle image using the maintained Dockerfile.bundle.
	docker build -f Dockerfile.bundle --build-arg VERSION=$(VERSION) --build-arg PACKAGE_NAME=$(PACKAGE_NAME) -t $(BUNDLE_IMG) .

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

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Install golangci-lint locally if needed.

$(GOLANGCI_LINT):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if needed.

$(KUSTOMIZE):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: operator-sdk
operator-sdk: $(OPERATOR_SDK) ## Download Operator SDK locally if needed.

$(OPERATOR_SDK):
	mkdir -p $(LOCALBIN)
	@tmp_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	curl -fsSL "https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_linux_amd64" -o "$$tmp_dir/operator-sdk_linux_amd64"; \
	curl -fsSL "https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/checksums.txt" -o "$$tmp_dir/checksums.txt"; \
	( cd "$$tmp_dir" && grep ' operator-sdk_linux_amd64$$' checksums.txt | sha256sum -c - ); \
	install -m 0755 "$$tmp_dir/operator-sdk_linux_amd64" "$(OPERATOR_SDK)"

.PHONY: envtest
envtest: $(ENVTEST) ## Install setup-envtest (downloads apiserver/etcd on first test run).

$(ENVTEST):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make <target>\n\nTargets:\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
