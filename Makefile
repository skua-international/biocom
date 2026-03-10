.PHONY: all build run clean test lint docker-build docker-run help

# Build variables
BINARY_NAME := biocom
BUILD_DIR := ./build
CMD_DIR := ./cmd/biocom
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags="-w -s -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

# Go variables
GOOS ?= linux
GOARCH ?= amd64

all: lint test build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -mod=vendor $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME)"

## run: Run the application locally
run:
	go run -mod=vendor $(CMD_DIR)/main.go

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

## docker-build: Build Docker image
docker-build:
	docker build -t biocom-bot:latest .

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
