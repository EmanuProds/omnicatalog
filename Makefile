# Makefile for omnicatalog

.PHONY: build test lint fmt vet clean install uninstall run daemon status models combos config schedule version

# Variables
BINARY_NAME := omnicatalog
BUILD_DIR := ./build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Default target
all: build

# Build binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/omnicatalog
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Production build (optimized)
build-prod:
	@echo "Building $(BINARY_NAME) (production)..."
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=1 go build -trimpath $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/omnicatalog
	@echo "Production build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Tests
test:
	@echo "Running tests..."
	@go test -v -race -count=1 ./...

test-coverage:
	@echo "Running tests with coverage..."
	@go test -v -race -count=1 -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage generated: coverage.html"

# Linting and formatting
lint:
	@echo "Running linters..."
	@golangci-lint run ./... || echo "golangci-lint not installed, skipping..."

fmt:
	@echo "Formatting code..."
	@go fmt ./...
	@goimports -w .

vet:
	@echo "Running go vet..."
	@go vet ./...

# Complete checks
check: fmt vet test
	@echo "All checks passed"

# Execution
run: build
	@echo "Running..."
	@$(BUILD_DIR)/$(BINARY_NAME) run

daemon: build
	@echo "Starting daemon..."
	@$(BUILD_DIR)/$(BINARY_NAME) daemon

status: build
	@$(BUILD_DIR)/$(BINARY_NAME) status

models: build
	@$(BUILD_DIR)/$(BINARY_NAME) models --validated

combos: build
	@$(BUILD_DIR)/$(BINARY_NAME) combos --show-current

config: build
	@$(BUILD_DIR)/$(BINARY_NAME) config validate

schedule: build
	@$(BUILD_DIR)/$(BINARY_NAME) schedule show

version: build
	@$(BUILD_DIR)/$(BINARY_NAME) version

# Systemd installation
install: build-prod
	@echo "Installing $(BINARY_NAME)..."
	@sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/
	@sudo cp deploy/omnicatalog.service /etc/systemd/system/
	@sudo systemctl daemon-reload
	@sudo systemctl enable --now omnicatalog
	@echo "Installed and started as systemd service"

uninstall:
	@echo "Removing $(BINARY_NAME)..."
	@sudo systemctl disable --now omnicatalog 2>/dev/null || true
	@sudo rm -f /etc/systemd/system/omnicatalog.service
	@sudo rm -f /usr/local/bin/$(BINARY_NAME)
	@sudo systemctl daemon-reload
	@echo "Removed"

# Cleanup
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@go clean -cache -modcache -testcache

# Dependencies
deps:
	@echo "Downloading dependencies..."
	@go mod download
	@go mod tidy
	@go mod verify

# Generate mocks for tests (requires mockgen)
mocks:
	@echo "Generating mocks..."
	@go generate ./...

# Help
help:
	@echo "omnicatalog - Makefile targets:"
	@echo ""
	@echo "  build          - Compile binary (dev)"
	@echo "  build-prod     - Compile optimized for production"
	@echo "  test           - Run tests"
	@echo "  test-coverage  - Run tests with HTML coverage"
	@echo "  lint           - Run golangci-lint"
	@echo "  fmt            - Format code (go fmt + goimports)"
	@echo "  vet            - Run go vet"
	@echo "  check          - fmt + vet + test"
	@echo "  run            - Build and execute 'run'"
	@echo "  daemon         - Build and execute 'daemon'"
	@echo "  status         - Build and execute 'status'"
	@echo "  models         - Build and list validated models"
	@echo "  combos         - Build and show combos"
	@echo "  config         - Build and validate config"
	@echo "  schedule       - Build and show schedule"
	@echo "  version        - Build and show version"
	@echo "  install        - Install as systemd service (requires sudo)"
	@echo "  uninstall      - Remove systemd service and binary"
	@echo "  clean          - Clean build artifacts"
	@echo "  deps           - Download and organize dependencies"
	@echo "  mocks          - Generate mocks for tests"
	@echo "  help           - Show this help"