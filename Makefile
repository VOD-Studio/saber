.PHONY: build build-prod clean test fmt lint run help

APP_NAME := saber
GIT_MSG := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
VERSION := 0.0.3
BUILD_DIR := bin
MAIN_FILE := main.go

# 生产构建标志
LDFLAGS := -s -w -X 'main.version=$(VERSION)' -X 'main.gitMsg=$(GIT_MSG)'

# 跨平台兼容
ifeq ($(OS),Windows_NT)
    MKDIR_P := cmd /c "if not exist $(BUILD_DIR) mkdir $(BUILD_DIR)"
else
    MKDIR_P := mkdir -p $(BUILD_DIR)
endif

build: ## Build the binary
	@$(MKDIR_P)
	go build -tags goolm -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .

build-prod: ## Build optimized production binary (static, stripped)
	@$(MKDIR_P)
	CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="$(LDFLAGS)" -gcflags="-l=4" -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .

clean: ## Remove build artifacts
ifeq ($(OS),Windows_NT)
	@powershell -Command "if (Test-Path $(BUILD_DIR)) { Remove-Item -Recurse -Force $(BUILD_DIR) }"
else
	@rm -rf $(BUILD_DIR)
endif

test: ## Run tests
	go test -v -tags goolm ./...

fmt: ## Format code with goimports
	goimports -w .

lint: ## Run golangci-lint
	golangci-lint run --build-tags goolm ./...

run: ## Run the application
	go run -tags goolm $(MAIN_FILE)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := build
