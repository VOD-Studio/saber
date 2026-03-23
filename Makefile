.PHONY: build build-prod clean test fmt lint run help bench bench-ai bench-matrix bench-mcp bench-profile bench-compare bench-save-baseline pprof-cpu pprof-mem bench-clean

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

build-all: ## 构建所有平台 (macOS/Linux/Windows, arm64/amd64)
	@mkdir -p $(BUILD_DIR)/release
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=darwin/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=darwin/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-darwin-arm64 .
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=linux/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-linux-arm64 .
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=windows/amd64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-windows-amd64.exe .
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -tags goolm -trimpath -ldflags="-s -w -v -X 'main.version=$(VERSION)' -X 'main.gitCommit=$(GIT_COMMIT)' -X 'main.gitBranch=$(GIT_BRANCH)' -X 'main.buildTime=$(BUILD_TIME)' -X 'main.goVersion=$(GO_VERSION)' -X 'main.buildPlatform=windows/arm64'" -gcflags="-l=4" -o $(BUILD_DIR)/release/$(APP_NAME)-windows-arm64.exe .

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

fmt: ## 使用 goimports 格式化代码
	goimports -w .

lint: ## 运行 golangci-lint 检查
	golangci-lint run --build-tags goolm ./...

run: ## 运行应用程序
	go run -tags goolm $(MAIN_FILE)

help: ## 显示帮助信息
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'

# 基准测试配置
BENCH_DIR := benchmark-results
BENCH_TIME ?= 3s

bench: ## 运行所有基准测试
	@mkdir -p $(BENCH_DIR)
	go test -tags goolm -bench=. -benchmem -benchtime=$(BENCH_TIME) ./... | tee $(BENCH_DIR)/results.txt

bench-ai: ## 运行 AI 包基准测试
	go test -tags goolm -v -bench=. -benchmem ./internal/ai/...

bench-matrix: ## 运行 Matrix 包基准测试
	go test -tags goolm -v -bench=. -benchmem ./internal/matrix/...

bench-mcp: ## 运行 MCP 包基准测试
	go test -tags goolm -v -bench=. -benchmem ./internal/mcp/...

bench-profile: ## 生成 CPU/内存分析文件
	@mkdir -p $(BENCH_DIR)
	go test -tags goolm -bench=. -benchmem -cpuprofile=$(BENCH_DIR)/cpu.prof -memprofile=$(BENCH_DIR)/mem.prof ./...
	@echo "Profile files generated in $(BENCH_DIR)/"
	@echo "View CPU profile: make pprof-cpu"
	@echo "View Memory profile: make pprof-mem"

bench-compare: ## 与基准比较性能
	@mkdir -p $(BENCH_DIR)
	@echo "Running current branch benchmarks..."
	go test -tags goolm -bench=. -benchmem ./... > $(BENCH_DIR)/current.txt
	@if [ -f $(BENCH_DIR)/baseline.txt ]; then \
		echo "Comparing with baseline..."; \
		echo "Install benchstat: go install golang.org/x/perf/cmd/benchstat@latest"; \
		benchstat $(BENCH_DIR)/baseline.txt $(BENCH_DIR)/current.txt 2>/dev/null || echo "benchstat not installed, install with: go install golang.org/x/perf/cmd/benchstat@latest"; \
	else \
		echo "No baseline found. Run 'make bench-save-baseline' first."; \
	fi

bench-save-baseline: ## 保存当前性能作为基准
	@mkdir -p $(BENCH_DIR)
	@echo "Saving baseline benchmarks..."
	go test -tags goolm -bench=. -benchmem -count=5 ./... > $(BENCH_DIR)/baseline.txt
	@echo "Baseline saved to $(BENCH_DIR)/baseline.txt"

pprof-cpu: ## 查看 CPU 分析
	@if [ ! -f $(BENCH_DIR)/cpu.prof ]; then \
		echo "Run 'make bench-profile' first"; \
		exit 1; \
	fi
	go tool pprof -http=:8080 $(BENCH_DIR)/cpu.prof

pprof-mem: ## 查看内存分析
	@if [ ! -f $(BENCH_DIR)/mem.prof ]; then \
		echo "Run 'make bench-profile' first"; \
		exit 1; \
	fi
	go tool pprof -http=:8080 $(BENCH_DIR)/mem.prof

bench-clean: ## 清理基准测试产物
	@rm -rf $(BENCH_DIR)

.DEFAULT_GOAL := build
