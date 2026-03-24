.PHONY: build build-prod clean test fmt lint run help

APP_NAME := saber
VERSION := 0.0.4
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d %H:%M:%S UTC')
GO_VERSION := $(shell go version | awk '{print $$3}')
BUILD_PLATFORM := $(shell go env GOOS)/$(shell go env GOARCH)
BUILD_DIR := bin
MAIN_FILE := main.go

# 生产构建标志
LDFLAGS := -s -w -v \
	-X 'main.version=$(VERSION)' \
	-X 'main.gitCommit=$(GIT_COMMIT)' \
	-X 'main.gitBranch=$(GIT_BRANCH)' \
	-X 'main.buildTime=$(BUILD_TIME)' \
	-X 'main.goVersion=$(GO_VERSION)' \
	-X 'main.buildPlatform=$(BUILD_PLATFORM)'

# 跨平台兼容
ifeq ($(OS),Windows_NT)
    MKDIR_P := cmd /c "if not exist $(BUILD_DIR) mkdir $(BUILD_DIR)"
else
    MKDIR_P := mkdir -p $(BUILD_DIR)
endif

build: ## 构建二进制文件
	@$(MKDIR_P)
	CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .

build-all: ## 构建所有平台 (macOS/Linux/Windows/FreeBSD/OpenBSD/Loong64, arm64/amd64)
	@mkdir -p $(BUILD_DIR)/release
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=darwin/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=darwin/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-darwin-arm64 .
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-arm64 .
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=windows/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=windows/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-windows-arm64.exe .
	GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=freebsd/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-freebsd-amd64 .
	GOOS=freebsd GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=freebsd/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-freebsd-arm64 .
	GOOS=openbsd GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=openbsd/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-openbsd-amd64 .
	GOOS=openbsd GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=openbsd/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-openbsd-arm64 .
	GOOS=linux GOARCH=loong64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/loong64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-loong64 .

build-prod: ## 构建优化的生产版本（静态链接，去除调试信息）
	@$(MKDIR_P)
	CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="$(LDFLAGS)" -gcflags="-l=4" -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .

clean: ## 清理构建产物
ifeq ($(OS),Windows_NT)
	@powershell -Command "if (Test-Path $(BUILD_DIR)) { Remove-Item -Recurse -Force $(BUILD_DIR) }"
	go clean -cache -testcache -modcache
else
	@rm -rf $(BUILD_DIR)
	go clean -cache -testcache -modcache
endif

test: ## 运行测试
	go test -v -tags goolm ./...

test-cover: ## 运行测试并生成 HTML 覆盖率报告
	go test -cover -coverprofile=coverage.out -tags goolm ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "覆盖率报告: coverage.html"

test-cover-func: ## 显示函数级别覆盖率详情
	go test -cover -coverprofile=coverage.out -tags goolm ./...
	go tool cover -func=coverage.out

test-cover-check: ## CI 覆盖率门禁检查（阈值 60%）
	@go test -cover -coverprofile=coverage.out -tags goolm ./... 2>/dev/null
	@total=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	if [ $$(echo "$$total < 60" | bc -L) -eq 1 ]; then \
		echo "覆盖率 $$total% 低于 60% 阈值"; exit 1; \
	fi; \
	echo "覆盖率 $$total% 达标"

fmt: ## 使用 goimports 格式化代码
	goimports -w .

lint: ## 运行 golangci-lint 检查
	golangci-lint run --build-tags goolm ./...

run: ## 运行应用程序
	go run -tags goolm $(MAIN_FILE)

help: ## 显示帮助信息
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := build
