# Repository Guidelines

Saber is a Matrix AI bot written in Go; module `rua.plus/saber`, Go 1.27.1. Every build, test, and editor session needs the `goolm` build tag (pure-Go E2EE, no CGO).

## Project Structure & Module Organization

- `main.go` — entry point; version metadata injected via `-ldflags`.
- `internal/bot/` — lifecycle: `Run()` → config → Matrix client → services → handlers → sync.
- `internal/ai/` — provider strategies, model registry, streaming, proactive chat, circuit breaker.
- `internal/matrix/` (+ `commands/`) — client, crypto, events, media, `!ping`/`!help`/`!ai`.
- `internal/mcp/` (+ `servers/`) — MCP manager, JSON Schema validation, JS sandbox, stdio/HTTP servers.
- `internal/persona/`, `internal/meme/`, `internal/config/`, `internal/cli/`, `internal/context/`, `internal/db/`.
- `docs/` — `comments.md` (comment spec), `prompts.md` (maintenance prompts).
- `bin/`, `coverage.*`, `config.yaml` are generated or secret: gitignored, never commit.

## Build, Test, and Development Commands

| Command | Purpose |
|---|---|
| `make build` | Static binary `bin/saber` (already uses `-gcflags="-l=4"`) |
| `make run` | Run locally via `go run -tags goolm main.go` |
| `make test` / `test-cover` | Run tests / write `coverage.html` |
| `make fmt` / `make lint` | goimports formatting; golangci-lint |
| `make deps` + `deps-verify` | Update deps with `GOFLAGS=-tags=goolm`, then tidy + build + test |
| `make build-all` | Cross-compile release binaries; `make help` lists all targets |

Outside Make, pass `-tags goolm` or export `GOFLAGS="-tags=goolm"`; `.vscode/settings.json` sets it for editors.

## Coding Style & Naming Conventions

- gofmt/goimports, tabs; CI fails when `gofmt -l .` is non-empty.
- Doc comments for exported identifiers are in Chinese and follow `docs/comments.md`: explain intent, keep in sync.
- Never ignore returned errors (`errcheck` plus `govet`, `ineffassign`, `staticcheck`, `unused`).
- Files are `snake_case.go`, one responsibility per file; shared fakes live in `testing_helpers.go`.

## Testing Guidelines

- Standard `testing` package, table-driven cases with `t.Run` subtests.
- Names: `TestFunction`, `TestReceiver_Method` (for example `TestCircuitState_String`), `BenchmarkXxx`.
- Tests needing a live homeserver are `t.Skip`ped with a reason, not deleted.
- Scope runs: `go test -v -tags goolm ./internal/ai/...`. CI gates at 60% total coverage (`make test-cover-check`), so cover new branches.

## Commit & Pull Request Guidelines

- Conventional Commits, package scope, Chinese summary: `feat(ai): 支持带上下文历史的对话`, `fix(matrix): 防止 nil client 导致 JoinRoom panic`. Types used: `feat` `fix` `refactor` `test` `docs` `chore` `ci` `build` `style` `sec`.
- **每完成一个功能点，提交一次**：一个可独立验证的改动（代码 + 对应测试，构建与测试均绿）完成后立即提交，不要把多个功能点攒成一次大提交。
- Target `master`. State motive and affected modules, link related issues, and include command output or log excerpts for behavior changes.
- Update `CHANGELOG.md` (Keep a Changelog + SemVer): the release job extracts `v*` notes from it. Refresh the README architecture tree when files move.
- CI must pass before review: gofmt, golangci-lint, `make test`, coverage gate, `make build`.

## Security & Configuration Tips

- Generate with `./bin/saber -generate-config`; keep it private via `chmod 600 config.yaml`.
- Session, credentials, and E2EE pickle files hold access tokens: `0600`, never committed; prefer tokens over passwords.
- Changes to the JS sandbox, stdio allowlist, or HTML sanitization are security-sensitive: flag them in the PR.
