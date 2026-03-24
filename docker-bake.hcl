// docker-bake.hcl - 多架构构建配置
// 使用: docker buildx bake

variable "REGISTRY" {
  default = ""
}

variable "VERSION" {
  default = "dev"
}

variable "GIT_COMMIT" {
  default = "unknown"
}

variable "GIT_BRANCH" {
  default = "unknown"
}

variable "BUILD_TIME" {
  default = ""
}

group "default" {
  targets = ["saber"]
}

target "saber" {
  context    = "."
  dockerfile = "Dockerfile"

  platforms = [
    "linux/amd64",
    "linux/arm64"
  ]

  tags = [
    "${REGISTRY}saber:${VERSION}",
    notequal("", REGISTRY) ? "${REGISTRY}saber:latest" : ""
  ]

  args = {
    VERSION    = VERSION
    GIT_COMMIT = GIT_COMMIT
    GIT_BRANCH = GIT_BRANCH
    BUILD_TIME = BUILD_TIME
  }

  labels = {
    "org.opencontainers.image.title"       = "Saber"
    "org.opencontainers.image.description" = "AI-powered Matrix bot"
    "org.opencontainers.image.version"     = VERSION
    "org.opencontainers.image.source"      = "https://github.com/user/saber"
  }
}

// 生产构建目标
target "prod" {
  inherits = ["saber"]
  args = {
    VERSION    = VERSION
    GIT_COMMIT = GIT_COMMIT
    GIT_BRANCH = GIT_BRANCH
    BUILD_TIME = timestamp()
  }
}