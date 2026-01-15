# SPDX-License-Identifier: Apache-2.0

.DEFAULT_GOAL := all

# Binary settings
BINARY_NAME := kube-compare-mcp
BUILD_DIR := bin
GO := go
GOFLAGS := -v

# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Container image settings
IMG ?= quay.io/$(USER)/kube-compare-mcp:$(VERSION)
CONTAINER_TOOL ?= podman
PLATFORM ?= linux/amd64

# Linter settings
GOLANGCI_LINT_VERSION ?= v2.1.6

# .PHONY declarations grouped by category
.PHONY: all build build-darwin-arm64 build-darwin-amd64 build-linux-amd64 build-all
.PHONY: clean install run
.PHONY: test test-cover vet fmt lint lint-fix verify mod-tidy ensure-golangci-lint
.PHONY: docker-build docker-push deploy undeploy setup-registry-credentials
.PHONY: help

all: build

## build: Build the MCP server binary for current platform
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/kube-compare-mcp

## build-darwin-arm64: Build for macOS Apple Silicon
build-darwin-arm64:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/kube-compare-mcp

## build-darwin-amd64: Build for macOS Intel
build-darwin-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/kube-compare-mcp

## build-linux-amd64: Build for Linux x86_64 (containers/servers)
build-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/kube-compare-mcp

## build-all: Build for all supported platforms
build-all: build-darwin-arm64 build-darwin-amd64 build-linux-amd64
	@echo "Built binaries for all platforms in $(BUILD_DIR)/"

## install: Install the binary to /usr/local/bin
install: build
	install -m 755 $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

## run: Run the server locally with stdio transport (for development)
run: build
	$(BUILD_DIR)/$(BINARY_NAME) --log-level=debug

## clean: Remove build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out
	$(GO) clean

## test: Run tests
test:
	$(GO) test -v ./...

## test-cover: Run tests with coverage
test-cover:
	$(GO) test -v -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

## vet: Run go vet
vet:
	$(GO) vet ./...

## fmt: Run gofmt and goimports
fmt:
	$(GO) fmt ./...
	@command -v goimports >/dev/null 2>&1 && goimports -w -local github.com/sakhoury/kube-compare-mcp . || echo "goimports not installed, skipping"

## ensure-golangci-lint: Install golangci-lint if not present
ensure-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found. Installing $(GOLANGCI_LINT_VERSION)..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	}

## lint: Run golangci-lint
lint: ensure-golangci-lint
	golangci-lint run ./...

## lint-fix: Run golangci-lint with auto-fix
lint-fix: ensure-golangci-lint
	golangci-lint run --fix ./...

## verify: Run all verification checks (fmt, vet, lint, test)
verify: fmt vet lint test
	@echo "All verification checks passed!"

## mod-tidy: Tidy go modules
mod-tidy:
	$(GO) mod tidy

## docker-build: Build container image for target platform (default: linux/amd64)
docker-build:
	$(CONTAINER_TOOL) build \
		--platform $(PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		-t $(IMG) .

## docker-push: Push the container image to registry
docker-push:
	$(CONTAINER_TOOL) push $(IMG)

## deploy: Deploy to OpenShift using kustomize
deploy:
	@echo "Deploying kube-compare-mcp to cluster..."
	@echo "Using image: $(IMG)"
	cd deploy && kustomize edit set image quay.io/REPLACE_WITH_YOUR_USERNAME/kube-compare-mcp=$(IMG)
	kubectl apply -k deploy/
	@cd deploy && git checkout kustomization.yaml 2>/dev/null || true

## undeploy: Remove deployment from OpenShift
undeploy:
	kubectl delete -k deploy/ --ignore-not-found=true

## setup-registry-credentials: Create registry credentials from OpenShift pull-secret (for RDS tools)
setup-registry-credentials:
	@echo "Creating registry-credentials secret from OpenShift pull-secret..."
	@kubectl create secret generic registry-credentials \
		--from-literal=.dockerconfigjson="$$(kubectl get secret pull-secret -n openshift-config -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d)" \
		--type=kubernetes.io/dockerconfigjson \
		-n kube-compare-mcp \
		--dry-run=client -o yaml | kubectl apply -f -
	@echo "Registry credentials configured successfully"

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
	@echo ""
	@echo "Variables:"
	@echo "  IMG                    Container image (default: quay.io/\$$(USER)/kube-compare-mcp:\$$(VERSION))"
	@echo "  CONTAINER_TOOL         Container tool (default: podman, can use docker)"
	@echo "  PLATFORM               Target platform for container build (default: linux/amd64)"
	@echo "  GOLANGCI_LINT_VERSION  golangci-lint version (default: $(GOLANGCI_LINT_VERSION))"
	@echo ""
	@echo "Examples:"
	@echo "  make verify                                                    # Run all checks"
	@echo "  make run                                                       # Run server locally"
	@echo "  make docker-build IMG=quay.io/myuser/kube-compare-mcp:v1.0.0   # Build container image"
	@echo "  make docker-push IMG=quay.io/myuser/kube-compare-mcp:v1.0.0    # Push to registry"
	@echo "  make deploy IMG=quay.io/myuser/kube-compare-mcp:v1.0.0         # Deploy to cluster"
	@echo ""
	@echo "Full deployment workflow (build, push, deploy, configure registry):"
	@echo "  make docker-build docker-push deploy setup-registry-credentials IMG=quay.io/myuser/kube-compare-mcp:latest"
