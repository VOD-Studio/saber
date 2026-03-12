.PHONY: build clean test fmt lint run help

APP_NAME := saber
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DIR := bin
MAIN_FILE := main.go

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build -tags goolm -o $(BUILD_DIR)/$(APP_NAME) .

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)

test: ## Run tests
	go test -v -tags goolm ./...

fmt: ## Format code
	go fmt ./...

lint: ## Run linter
	go vet -tags goolm ./...

run: ## Run the application
	go run -tags goolm $(MAIN_FILE)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := build
