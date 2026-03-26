<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# docs

## Purpose

项目文档目录，包含代码规范、开发指南等文档。

## Key Files

| File | Description |
|------|-------------|
| `comments.md` | Go 代码注释规范，定义项目的文档风格 |

## For AI Agents

### Working In This Directory

- **中文注释要求**: 所有代码注释必须使用中文，遵循 `comments.md` 中的规范
- **导出标识符**: 所有 exported 名称必须有注释，注释以标识符名称开头

### Common Patterns

#### 包注释格式

```go
// Package <name> 提供功能简述。
//
// 详细描述段落。
package <name>
```

#### 函数注释格式

```go
// <FunctionName> 提供功能简述。
//
// 参数:
//   - param1: 参数描述
//
// 返回值:
//   - error: 错误描述
func <FunctionName>(param1 type) error { ... }
```

#### 错误变量格式

```go
var (
    // ErrNotFound 表示请求的资源未找到。
    ErrNotFound = errors.New("resource not found")
)
```

## Dependencies

### Internal

无

### External

无

<!-- MANUAL: -->