IMG ?= ghcr.io/slauger/crashloop-operator:latest
NAMESPACE ?= crashloop-system
CONTAINER_TOOL ?= $(shell which podman 2>/dev/null || which docker 2>/dev/null)
CONTROLLER_GEN = go tool controller-gen
GOVULNCHECK = go tool govulncheck
SETUP_ENVTEST = go tool setup-envtest
ENVTEST_K8S_VERSION ?= 1.37.0
COVERAGE_THRESHOLD ?= 55

.PHONY: all
all: build

##@ Development

.PHONY: manifests
manifests: ## Generate CRD and RBAC manifests.
	$(CONTROLLER_GEN) crd paths="./..." output:crd:dir=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=crashloop-operator paths="./..." output:rbac:dir=config/rbac
	cp config/crd/bases/*.yaml charts/crashloop-operator/crds/

.PHONY: generate
generate: ## Generate deepcopy methods.
	$(CONTROLLER_GEN) object paths="./api/..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest-assets ## Run tests.
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test ./... -race -covermode=atomic -coverprofile cover.out

.PHONY: envtest-assets
envtest-assets: ## Download the envtest control plane binaries.
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path >/dev/null

.PHONY: check-coverage
check-coverage: test ## Run tests and enforce the coverage threshold.
	./hack/check-coverage.sh cover.out $(COVERAGE_THRESHOLD)

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build operator binary.
	go build -o bin/manager ./cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run the operator locally against the configured cluster.
	go run ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build container image.
	$(CONTAINER_TOOL) build -t $(IMG) -f images/crashloop-operator/Containerfile .

.PHONY: docker-push
docker-push: ## Push container image.
	$(CONTAINER_TOOL) push $(IMG)

##@ Deployment

.PHONY: install
install: manifests ## Install operator via Helm.
	# The chart defaults image.tag to appVersion, which is 0.0.0 in git, so a
	# local install has to name the image explicitly.
	helm upgrade --install crashloop-operator charts/crashloop-operator \
		--namespace $(NAMESPACE) --create-namespace \
		--set image.repository=$(firstword $(subst :, ,$(IMG))) \
		--set image.tag=$(lastword $(subst :, ,$(IMG)))

.PHONY: uninstall
uninstall: ## Remove operator and CRDs from the cluster.
	-helm uninstall crashloop-operator --namespace $(NAMESPACE) 2>/dev/null
	-kubectl delete -f config/crd/bases/ --ignore-not-found

##@ Helm

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart.
	helm lint charts/crashloop-operator

.PHONY: helm-template
helm-template: ## Render Helm chart templates locally.
	helm template crashloop-operator charts/crashloop-operator

.PHONY: helm-unittest
helm-unittest: ## Run Helm chart unit tests.
	helm unittest charts/crashloop-operator

# renovate: datasource=github-releases depName=norwoodj/helm-docs
HELM_DOCS_VERSION ?= 1.14.2
# renovate: datasource=github-releases depName=losisin/helm-values-schema-json
HELM_SCHEMA_VERSION ?= 2.6.0
TOOL_OS = $(shell uname -s | tr '[:upper:]' '[:lower:]')
TOOL_ARCH = $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
# helm-docs names its assets Darwin/Linux with the raw x86_64 arch, while the
# schema tool uses lowercase os with amd64. They need separate variables.
HELM_DOCS_OS = $(shell uname -s)
HELM_DOCS_ARCH = $(shell uname -m | sed -e 's/aarch64/arm64/')

.PHONY: helm-docs-bin
helm-docs-bin: ## Download helm-docs into bin/.
	@test -x bin/helm-docs || { \
		mkdir -p bin; \
		archive="helm-docs_$(HELM_DOCS_VERSION)_$(HELM_DOCS_OS)_$(HELM_DOCS_ARCH).tar.gz"; \
		base="https://github.com/norwoodj/helm-docs/releases/download/v$(HELM_DOCS_VERSION)"; \
		curl -sSLo "bin/$$archive" "$$base/$$archive"; \
		curl -sSLo bin/helm-docs-checksums.txt "$$base/checksums.txt"; \
		(cd bin && grep -E "  $$archive$$" helm-docs-checksums.txt | shasum -a 256 --check); \
		tar -xzf "bin/$$archive" -C bin helm-docs; \
		rm -f "bin/$$archive" bin/helm-docs-checksums.txt; \
	}

.PHONY: helm-schema-bin
helm-schema-bin: ## Download helm-values-schema-json into bin/.
	@test -x bin/helm-schema || { \
		mkdir -p bin; \
		archive="helm-values-schema-json_$(HELM_SCHEMA_VERSION)_$(TOOL_OS)_$(TOOL_ARCH).tgz"; \
		base="https://github.com/losisin/helm-values-schema-json/releases/download/v$(HELM_SCHEMA_VERSION)"; \
		curl -sSLo "bin/$$archive" "$$base/$$archive"; \
		curl -sSLo bin/schema-checksums.sha "$$base/helm-values-schema-json-checksum.sha"; \
		(cd bin && grep -E "  $$archive$$" schema-checksums.sha | shasum -a 256 --check); \
		tar -xzf "bin/$$archive" -C bin schema; \
		mv bin/schema bin/helm-schema; \
		rm -f "bin/$$archive" bin/schema-checksums.sha; \
	}

.PHONY: helm-docs
helm-docs: helm-docs-bin ## Regenerate the chart README from values.yaml.
	bin/helm-docs --chart-search-root charts

.PHONY: helm-schema
helm-schema: helm-schema-bin ## Regenerate values.schema.json from values.yaml.
	cd charts/crashloop-operator && $(PWD)/bin/helm-schema -f values.yaml --use-helm-docs

##@ CI

GOLANGCI_LINT ?= $(shell which golangci-lint 2>/dev/null)

.PHONY: lint
lint: ## Run golangci-lint.
	@if [ -z "$(GOLANGCI_LINT)" ]; then \
		echo "error: golangci-lint not found on PATH."; \
		echo "Install it from https://golangci-lint.run/welcome/install/ (CI pins the version in .github/workflows/_go.yaml)."; \
		exit 1; \
	fi
	$(GOLANGCI_LINT) run ./...

.PHONY: vulncheck
vulncheck: ## Run govulncheck.
	$(GOVULNCHECK) ./...

GENERATED_PATHS = api/v1alpha1/zz_generated.deepcopy.go config/crd/bases config/rbac charts/crashloop-operator/crds

.PHONY: check-tidy
check-tidy: ## Check go.mod and go.sum are tidy.
	@go mod tidy
	@if ! git diff HEAD --quiet -- go.mod go.sum; then \
		echo "error: go.mod or go.sum is not tidy. Run 'go mod tidy' and commit the result."; \
		git diff HEAD --stat -- go.mod go.sum; \
		exit 1; \
	fi

.PHONY: check-manifests
check-manifests: manifests generate ## Check for CRD and deepcopy drift.
	@if ! git diff --quiet -- $(GENERATED_PATHS); then \
		echo "error: generated files are out of date. Run 'make manifests generate' and commit the result."; \
		git diff --stat -- $(GENERATED_PATHS); \
		exit 1; \
	fi

.PHONY: shellcheck
shellcheck: ## Lint the shell scripts in hack/.
	@if ! command -v shellcheck >/dev/null 2>&1; then \
		echo "error: shellcheck not found on PATH."; \
		echo "Install it from https://github.com/koalaman/shellcheck#installing."; \
		exit 1; \
	fi
	shellcheck hack/*.sh

.PHONY: check-rbac
check-rbac: manifests ## Check the chart RBAC matches the kubebuilder markers.
	./hack/check-rbac.sh

.PHONY: check-helm-docs
check-helm-docs: helm-docs helm-schema ## Check the chart README and schema are up to date.
	@if ! git diff HEAD --quiet -- charts/crashloop-operator/README.md charts/crashloop-operator/values.schema.json; then \
		echo "error: chart README or values.schema.json is out of date. Run 'make helm-docs helm-schema' and commit the result."; \
		git diff HEAD --stat -- charts/crashloop-operator/README.md charts/crashloop-operator/values.schema.json; \
		exit 1; \
	fi

.PHONY: ci
ci: lint vet shellcheck check-tidy check-coverage check-manifests vulncheck helm-lint helm-unittest check-helm-docs check-rbac ## Run all CI checks locally.
	@echo "All CI checks passed."

##@ E2E

# renovate: datasource=github-releases depName=kyverno/chainsaw
CHAINSAW_VERSION ?= 0.2.15
KIND_CLUSTER ?= crashloop-e2e
CHAINSAW_OS = $(shell uname -s | tr '[:upper:]' '[:lower:]')
CHAINSAW_ARCH = $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

.PHONY: chainsaw
chainsaw: ## Download the chainsaw CLI into bin/.
	@test -x bin/chainsaw || { \
		mkdir -p bin; \
		archive="chainsaw_$(CHAINSAW_OS)_$(CHAINSAW_ARCH).tar.gz"; \
		base="https://github.com/kyverno/chainsaw/releases/download/v$(CHAINSAW_VERSION)"; \
		curl -sSLo "bin/$$archive" "$$base/$$archive"; \
		curl -sSLo bin/chainsaw-checksums.txt "$$base/checksums.txt"; \
		(cd bin && grep -E "  $$archive$$" chainsaw-checksums.txt | shasum -a 256 --check); \
		tar -xzf "bin/$$archive" -C bin chainsaw; \
		rm -f "bin/$$archive" bin/chainsaw-checksums.txt; \
	}

.PHONY: e2e-cluster
e2e-cluster: ## Create the kind cluster used by the e2e tests.
	kind create cluster --name $(KIND_CLUSTER) --config tests/e2e/kind-config.yaml

.PHONY: e2e-deploy
e2e-deploy: docker-build ## Build the image, load it into kind and install the chart.
	kind load docker-image $(IMG) --name $(KIND_CLUSTER)
	helm upgrade --install crashloop-operator charts/crashloop-operator \
		--namespace $(NAMESPACE) --create-namespace \
		--set image.repository=$(firstword $(subst :, ,$(IMG))) \
		--set image.tag=$(lastword $(subst :, ,$(IMG))) \
		--set image.pullPolicy=Never \
		--wait

.PHONY: e2e-run
e2e-run: chainsaw ## Run the chainsaw e2e tests against the current cluster.
	bin/chainsaw test tests/e2e --config tests/e2e/chainsaw-config.yaml

.PHONY: e2e
e2e: e2e-deploy e2e-run ## Deploy into the current cluster and run the e2e tests.

.PHONY: e2e-clean
e2e-clean: ## Delete the kind cluster.
	kind delete cluster --name $(KIND_CLUSTER)


##@ Help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
