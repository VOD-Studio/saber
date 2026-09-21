.PHONY: build build-all build-prod build-freebsd build-openbsd build-loong64 clean test fmt fmt-check lint lint-fix lint-install check run deps deps-check deps-verify help docker-build docker-buildx docker-push docker-load docker-run docker-clean

APP_NAME := saber
VERSION := 0.0.5
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d %H:%M:%S UTC')
GO_VERSION := $(shell go version | awk '{print $$3}')
BUILD_PLATFORM := $(shell go env GOOS)/$(shell go env GOARCH)
BUILD_DIR := bin
MAIN_FILE := main.go

# 工具版本：golangci-lint 只能用「不低于本机 Go」的版本构建，
# 否则解析新版标准库时会 panic，所以这里钉住版本并用 make lint-install 安装。
GOLANGCI_LINT_VERSION := 2.13.2
GOLANGCI_LINT ?= golangci-lint

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

build: ## 构建优化的生产版本（静态链接，去除调试信息）
	@$(MKDIR_P)
	CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="$(LDFLAGS)" -gcflags="-l=4" -o $(BUILD_DIR)/$(APP_NAME)$(shell go env GOEXE) .

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

build-freebsd: ## 构建 FreeBSD 平台二进制文件 (amd64/arm64)
	@mkdir -p $(BUILD_DIR)/release
	GOOS=freebsd GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=freebsd/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-freebsd-amd64 .
	GOOS=freebsd GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=freebsd/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-freebsd-arm64 .

build-openbsd: ## 构建 OpenBSD 平台二进制文件 (amd64/arm64)
	@mkdir -p $(BUILD_DIR)/release
	GOOS=openbsd GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=openbsd/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-openbsd-amd64 .
	GOOS=openbsd GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=openbsd/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-openbsd-arm64 .

build-loong64: ## 构建龙芯 LoongArch64 平台二进制文件
	@mkdir -p $(BUILD_DIR)/release
	GOOS=linux GOARCH=loong64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/loong64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-loong64 .

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

fmt-check: ## 检查代码格式（与 CI 的 gofmt 门禁一致）
	@test -z "$$(gofmt -l .)" || { echo "以下文件未格式化:"; gofmt -l .; exit 1; }

lint: ## 运行 golangci-lint 检查（tags/timeout 统一由 .golangci.yml 提供）
	@$(GOLANGCI_LINT) --version | grep -Eq 'version v?$(GOLANGCI_LINT_VERSION)([[:space:]]|$$)' || { \
		echo "golangci-lint 版本不是 v$(GOLANGCI_LINT_VERSION)，请运行 make lint-install"; exit 1; }
	$(GOLANGCI_LINT) run ./...

lint-fix: ## 运行 golangci-lint 并自动修复可修复项
	$(GOLANGCI_LINT) run --fix ./...

lint-install: ## 用本机 Go 重新构建并安装 golangci-lint
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)

check: fmt-check lint test ## 提交前的本地门禁（格式 + lint + 测试）

run: ## 运行应用程序
	go run -tags goolm $(MAIN_FILE)

# ============================================
# 依赖管理
# ============================================
# 说明：go get / go mod tidy 会按 build constraints 决定依赖图，
# 因此统一通过 GOFLAGS 带上 goolm 标签，避免 E2EE 相关依赖被漏掉。

deps: ## 更新所有依赖（minor/patch）并整理 go.mod、go.sum
	GOFLAGS="-tags=goolm" go get -u ./...
	GOFLAGS="-tags=goolm" go mod tidy
	@echo "依赖已更新，运行 make deps-verify 验证"

deps-check: ## 检查哪些依赖有可用更新
	@GOFLAGS="-tags=goolm" go list -m -u all | grep -E '\[[^]]*\]' || echo "所有依赖均为最新"

deps-verify: ## 校验依赖：go.mod 是否 tidy + 编译 + 测试
	@GOFLAGS="-tags=goolm" go mod tidy -diff
	GOFLAGS="-tags=goolm" go mod verify
	CGO_ENABLED=0 go build -tags goolm ./...
	go test -tags goolm ./...

# ============================================
# Docker 构建命令
# ============================================

DOCKER_REGISTRY ?=
IMAGE_NAME := saber

docker-build: ## 构建 Docker 镜像（当前平台）
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg GIT_BRANCH=$(GIT_BRANCH) \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		-t $(IMAGE_NAME):$(VERSION) \
		-t $(IMAGE_NAME):latest \
		.

docker-buildx: ## 构建多架构 Docker 镜像（amd64 + arm64）
	docker buildx bake \
		--set saber.platform=linux/amd64,linux/arm64 \
		--set saber.tags=$(IMAGE_NAME):$(VERSION) \
		--set saber.tags=$(IMAGE_NAME):latest

docker-push: ## 构建并推送多架构镜像到仓库
	@if [ -z "$(DOCKER_REGISTRY)" ]; then \
		echo "错误: 请设置 DOCKER_REGISTRY 变量"; \
		exit 1; \
	fi
	docker buildx bake \
		--push \
		--set saber.platform=linux/amd64,linux/arm64 \
		--set saber.tags=$(DOCKER_REGISTRY)/$(IMAGE_NAME):$(VERSION) \
		--set saber.tags=$(DOCKER_REGISTRY)/$(IMAGE_NAME):latest

docker-load: ## 构建多架构镜像并加载到本地
	docker buildx bake \
		--load \
		--set saber.platform=linux/$(shell go env GOARCH) \
		--set saber.tags=$(IMAGE_NAME):$(VERSION)

docker-run: ## 运行 Docker 容器（开发环境）
	docker run --rm -it \
		-v $(PWD)/config.yaml:/data/config.yaml:ro \
		-v $(PWD)/data:/data \
		--name saber \
		$(IMAGE_NAME):latest

docker-clean: ## 清理 Docker 构建缓存
	docker builder prune -f

help: ## 显示帮助信息
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := build
