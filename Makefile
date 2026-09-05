IMG ?= ghcr.io/slauger/crashloop-operator:latest
NAMESPACE ?= crashloop-system
CONTAINER_TOOL ?= $(shell which podman 2>/dev/null || which docker 2>/dev/null)
CONTROLLER_GEN = go tool controller-gen
GOVULNCHECK = go tool govulncheck
COVERAGE_THRESHOLD ?= 55

.PHONY: all
all: build

##@ Development

.PHONY: manifests
manifests: ## Generate CRD manifests.
	$(CONTROLLER_GEN) crd paths="./..." output:crd:dir=config/crd/bases
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
test: manifests generate fmt vet ## Run tests.
	go test ./... -race -covermode=atomic -coverprofile cover.out

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
	helm upgrade --install crashloop-operator charts/crashloop-operator \
		--namespace $(NAMESPACE) --create-namespace

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

GENERATED_PATHS = api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/crashloop-operator/crds

.PHONY: check-manifests
check-manifests: manifests generate ## Check for CRD and deepcopy drift.
	@if ! git diff --quiet -- $(GENERATED_PATHS); then \
		echo "error: generated files are out of date. Run 'make manifests generate' and commit the result."; \
		git diff --stat -- $(GENERATED_PATHS); \
		exit 1; \
	fi

.PHONY: ci
ci: lint vet check-coverage check-manifests vulncheck helm-lint helm-unittest ## Run all CI checks locally.
	@echo "All CI checks passed."

##@ Help

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
