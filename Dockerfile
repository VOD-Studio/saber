# 阶段1: 构建
FROM golang:1.24-alpine AS builder

WORKDIR /build

# 安装构建依赖
RUN apk add --no-cache git

# 复制 go.mod 和 go.sum 以利用缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建参数：版本信息
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG GIT_BRANCH=unknown
ARG BUILD_TIME=unknown

# 构建（静态链接，纯 Go）
RUN CGO_ENABLED=0 GOOS=linux go build \
    -tags goolm \
    -trimpath \
    -ldflags="-s -w \
        -X 'main.version=${VERSION}' \
        -X 'main.gitCommit=${GIT_COMMIT}' \
        -X 'main.gitBranch=${GIT_BRANCH}' \
        -X 'main.buildTime=${BUILD_TIME}' \
        -extldflags '-static'" \
    -gcflags="-l=4" \
    -o saber .

# 阶段2: 运行（Distroless 镜像）
# static-debian12 已包含 CA 证书、时区数据、非 root 用户
FROM gcr.io/distroless/static-debian12:latest

# 复制二进制
COPY --from=builder /build/saber /saber

# 数据目录
VOLUME ["/data"]
WORKDIR /data

# Distroless 默认使用非 root 用户 (nonroot:nonroot)
# 无需手动配置用户

ENTRYPOINT ["/saber"]