# AGENTS.md

This file provides guidance to AI coding agents working in this repository.

## Project Overview

**Saber** — AI-powered Matrix bot using Go 1.26.1 and mautrix SDK.
Module: `rua.plus/saber`

**Architecture**: `main.go` → `internal/bot` → `internal/{matrix,ai,config,cli,mcp,db,meme}`

**Key deps**: `maunium.net/go/mautrix`, `github.com/sashabaranov/go-openai`, `github.com/modelcontextprotocol/go-sdk`

---

## Build, Test, Lint

**Build tag required for E2EE**: `-tags goolm`

```bash
make build                              # → bin/saber
make build-prod                         # Production (pure Go, static)
make test                               # All tests
go test -v -tags goolm ./internal/ai    # Single package
go test -v -tags goolm ./internal/ai -run TestService    # Single test
go test -cover -race -tags goolm ./...  # Coverage + race
make lint                               # golangci-lint
make run                                # Run app
```

**Editor config**: `export GOFLAGS="-tags=goolm"`

---

## Code Style

### Formatting & Naming

- **Indent**: Tab (4-space width)
- **Imports**: Stdlib → External → Internal (blank lines between groups)
- **Names**: Package lowercase, exported PascalCase, unexported camelCase
- **Errors**: `Err` prefix | **Strategies/Factories**: `*Strategy`, `*Factory` suffix

### Error Handling & Logging

```go
// Wrap with context using %w
if err != nil {
    return fmt.Errorf("failed to load: %w", err)
}

// Use slog structured logging, never log secrets
slog.Info("service initialized", "model", cfg.DefaultModel)
```

### Comments

**All comments in Chinese.** All exported identifiers must have comments starting with the identifier name. See [`docs/comments.md`](docs/comments.md) for details.

### Import Groups

```go
import (
    "context"
    "fmt"

    "github.com/sashabaranov/go-openai"
    "maunium.net/go/mautrix"

    "rua.plus/saber/internal/config"
)
```

---

## Architecture Patterns

### Initialization Order

`Run() → initConfig() → initMatrixClient() → initServices() → setupEventHandlers() → startSync()`

**Service deps**: MCPManager → AIService (AI needs MCP tools); MediaService before AIService; ProactiveManager last.

### Key Patterns

- **Strategy (AI Clients)**: `ClientStrategy` interface → `factory.GetDefaultFactory().RegisterStrategy()`. Built-in: `openai`, `azure`
- **Factory (MCP Servers)**: `MCPServerFactory` interface. Types: `builtin`, `stdio`, `http`
- **Circuit Breaker**: `NewCircuitBreaker(5, 30*time.Second)` → `cb.Allow()`, `cb.RecordSuccess/Failure()`
- **Shared HTTP Client**: `servers.GetSharedHTTPClient()` for all MCP servers
- **Context Metadata**: `ai.WithUserContext()`, `matrix.WithEventID()`, `ai.GetUserFromContext()`

### SQLite Dual Driver

| File | Build tag | Driver |
|------|-----------|--------|
| `sqlite_cgo.go` | CGO_ENABLED=1 | `mattn/go-sqlite3` |
| `sqlite_nocgo.go` | CGO_ENABLED=0 | `modernc/sqlite` |

---

## Testing

### Naming & Structure

`Test<FunctionName>_<Scenario>` — Helper prefix: `create` or `new`

```go
func TestNewService_NilConfig(t *testing.T) { ... }

func TestValidate(t *testing.T) {
    tests := []struct {
        name    string
        config  Config
        wantErr bool
    }{
        {"valid", Config{URL: "https://example.com"}, false},
        {"missing URL", Config{}, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if (tt.config.Validate() != nil) != tt.wantErr {
                t.Fail()
            }
        })
    }
}
```

**Requirements**: Cover success/failure paths | Use `t.TempDir()` | Table-driven | Independent tests

---

## Security

**Never commit**: `config.yaml`, `*.session`, `*.key`, `*.db`

**Config permissions**: `0o600` | **Auth priority**: access_token > password

**stdio MCP**: Must configure `allowed_commands` whitelist

**HTML output**: Use `matrix.SanitizeHTML()` to prevent XSS

---

## Development

```bash
git checkout -b feature/description
make lint && make test
git commit -m "feat(scope): description"  # <type>(<scope>): <subject>
```

---

## References

- [Effective Go](https://go.dev/doc/effective_go)
- [mautrix-go](https://docs.mau.fi/mautrix-go/)
- [go-openai](https://pkg.go.dev/github.com/sashabaranov/go-openai)
- [MCP Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk)
