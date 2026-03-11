.PHONY: all build build-bot build-watchdog run clean test lint docker-build docker-run help

# Build variables
BUILD_DIR := ./build
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags="-w -s -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

# Go variables
GOOS ?= linux
GOARCH ?= amd64

all: lint test build

## build: Build both binaries
build: build-bot build-watchdog

## build-bot: Build the bot binary
build-bot:
	@echo "Building biocom..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -mod=vendor $(LDFLAGS) -o $(BUILD_DIR)/biocom ./cmd/biocom
	@echo "Built: $(BUILD_DIR)/biocom"

## build-watchdog: Build the watchdog binary
build-watchdog:
	@echo "Building watchdog..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -mod=vendor $(LDFLAGS) -o $(BUILD_DIR)/watchdog ./cmd/watchdog
	@echo "Built: $(BUILD_DIR)/watchdog"

## run: Run the bot locally
run:
	go run -mod=vendor ./cmd/biocom/main.go

## run-watchdog: Run the watchdog locally
run-watchdog:
	go run -mod=vendor ./cmd/watchdog/main.go

## test: Run tests
test:
	go test -mod=vendor -v -race -cover ./...

## lint: Run linters
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run ./...

## fmt: Format code
fmt:
	go fmt ./...
	goimports -w .

## clean: Clean build artifacts
clean:
	@rm -rf $(BUILD_DIR)
	@go clean -cache
	@echo "Cleaned."

## deps: Vendor dependencies
deps:
	go mod tidy
	go mod vendor

## docker-build: Build both Docker images
docker-build:
	docker build --target biocom -t biocom-bot:latest .
	docker build --target watchdog -t biocom-watchdog:latest .

## docker-run: Run with Docker Compose
docker-run:
	docker compose up -d

## docker-logs: View container logs
docker-logs:
	docker compose logs -f

## docker-stop: Stop containers
docker-stop:
	docker compose down

## docker-rebuild: Rebuild and restart containers
docker-rebuild: docker-stop docker-build docker-run

help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
