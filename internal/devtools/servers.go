package devtools

import (
	"path/filepath"
	"sort"
	"strings"

	"rua.plus/saber/internal/config"
)

// Server 描述实际 stdio 启动命令、语言范围及可选显式安装命令。
type Server struct {
	ID                    string         `json:"id"`
	Command               []string       `json:"-"`
	Extensions            []string       `json:"extensions"`
	RootMarkers           []string       `json:"root_markers,omitempty"`
	InstallCommand        []string       `json:"-"`
	InitializationOptions map[string]any `json:"-"`
	Settings              map[string]any `json:"-"`
	Disabled              bool           `json:"disabled"`
}

func builtinServers() []Server {
	npm := func(packages ...string) []string {
		return append([]string{"npm", "install", "--global", "--ignore-scripts", "--"}, packages...)
	}
	return []Server{
		{ID: "pyright", Command: []string{"pyright-langserver", "--stdio"}, Extensions: []string{".py", ".pyi"}, InstallCommand: npm("pyright")},
		{ID: "typescript", Command: []string{"typescript-language-server", "--stdio"}, Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}, InstallCommand: npm("typescript-language-server", "typescript@6")},
		{ID: "vue-language-server", Command: []string{"vue-language-server", "--stdio"}, Extensions: []string{".vue"}, InstallCommand: npm("@vue/language-server@2", "typescript@6"), InitializationOptions: map[string]any{"vue": map[string]any{"hybridMode": false}}},
		{ID: "svelte-language-server", Command: []string{"svelteserver", "--stdio"}, Extensions: []string{".svelte"}, InstallCommand: npm("svelte-language-server")},
		{ID: "astro-language-server", Command: []string{"astro-ls", "--stdio"}, Extensions: []string{".astro"}, InstallCommand: npm("@astrojs/language-server")},
		{ID: "gopls", Command: []string{"gopls"}, Extensions: []string{".go"}, InstallCommand: []string{"go", "install", "golang.org/x/tools/gopls@latest"}},
		{ID: "rust-analyzer", Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}},
		{ID: "clangd", Command: []string{"clangd", "--background-index", "--clang-tidy"}, Extensions: []string{".c", ".cpp", ".cc", ".cxx", ".h", ".hh", ".hpp", ".hxx"}},
		{ID: "bash-language-server", Command: []string{"bash-language-server", "start"}, Extensions: []string{".sh", ".bash", ".zsh", ".ksh"}, InstallCommand: npm("bash-language-server")},
		{ID: "yaml-language-server", Command: []string{"yaml-language-server", "--stdio"}, Extensions: []string{".yaml", ".yml"}, InstallCommand: npm("yaml-language-server")},
		{ID: "lua-language-server", Command: []string{"lua-language-server"}, Extensions: []string{".lua"}},
		{ID: "laravel-lsp", Command: []string{"laravel-lsp", "lsp"}, Extensions: []string{".blade.php"}},
		{ID: "intelephense", Command: []string{"intelephense", "--stdio"}, Extensions: []string{".php"}, InstallCommand: npm("intelephense"), InitializationOptions: map[string]any{"telemetry": map[string]any{"enabled": false}}},
		{ID: "ocaml-lsp", Command: []string{"ocamllsp"}, Extensions: []string{".ml", ".mli"}},
		{ID: "dockerfile-ls", Command: []string{"docker-langserver", "--stdio"}, Extensions: []string{".dockerfile", "Dockerfile"}, InstallCommand: npm("dockerfile-language-server-nodejs")},
		{ID: "terraform-ls", Command: []string{"terraform-ls", "serve"}, Extensions: []string{".tf", ".tfvars"}, InitializationOptions: map[string]any{"experimentalFeatures": map[string]any{"prefillRequiredFields": true, "validateOnSave": true}}},
		{ID: "dart", Command: []string{"dart", "language-server", "--lsp"}, Extensions: []string{".dart"}},
		{ID: "haskell-language-server", Command: []string{"haskell-language-server-wrapper", "--lsp"}, Extensions: []string{".hs", ".lhs"}},
		{ID: "julia", Command: []string{"julia", "--startup-file=no", "--history-file=no", "-e", "using LanguageServer; runserver()"}, Extensions: []string{".jl"}},
		{ID: "clojure-lsp", Command: []string{"clojure-lsp", "listen"}, Extensions: []string{".clj", ".cljs", ".cljc", ".edn"}},
		{ID: "nixd", Command: []string{"nixd"}, Extensions: []string{".nix"}},
		{ID: "zls", Command: []string{"zls"}, Extensions: []string{".zig", ".zon"}},
		{ID: "gleam", Command: []string{"gleam", "lsp"}, Extensions: []string{".gleam"}},
		{ID: "elixir-ls", Command: []string{"elixir-ls"}, Extensions: []string{".ex", ".exs"}},
		{ID: "prisma", Command: []string{"prisma", "language-server"}, Extensions: []string{".prisma"}},
		{ID: "kotlin-language-server", Command: []string{"kotlin-language-server"}, Extensions: []string{".kt", ".kts"}},
		{ID: "jdtls", Command: []string{"jdtls"}, Extensions: []string{".java"}},
		{ID: "powershell", Extensions: []string{".ps1", ".psm1", ".psd1"}},
	}
}
func configuredServers(cfg map[string]config.LanguageServerConfig) []Server {
	servers := builtinServers()
	known := make(map[string]bool)
	for i := range servers {
		s := &servers[i]
		s.RootMarkers = serverMarkers[s.ID]
		known[s.ID] = true
		if override, ok := cfg[s.ID]; ok {
			applyServerOverride(s, override)
		}
	}
	var ids []string
	for id := range cfg {
		if !known[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := Server{ID: id}
		applyServerOverride(&s, cfg[id])
		servers = append(servers, s)
	}
	return servers
}
func applyServerOverride(s *Server, c config.LanguageServerConfig) {
	s.Disabled = c.Disabled
	if len(c.Command) > 0 {
		s.Command = append([]string(nil), c.Command...)
	}
	if len(c.Extensions) > 0 {
		s.Extensions = append([]string(nil), c.Extensions...)
	}
	if len(c.RootMarkers) > 0 {
		s.RootMarkers = append([]string(nil), c.RootMarkers...)
	}
	if c.InitializationOptions != nil {
		s.InitializationOptions = mergeOptions(s.InitializationOptions, c.InitializationOptions)
	}
	if c.Settings != nil {
		s.Settings = c.Settings
	}
}
func matchesServer(s Server, path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	for _, ext := range s.Extensions {
		if strings.HasPrefix(ext, ".") {
			if strings.HasSuffix(lower, strings.ToLower(ext)) {
				return true
			}
		} else if strings.EqualFold(ext, lower) {
			return true
		}
	}
	return false
}
func languageID(path string, override config.LanguageServerConfig) string {
	if override.LanguageID != "" {
		return override.LanguageID
	}
	ext := strings.ToLower(filepath.Ext(path))
	if strings.HasSuffix(strings.ToLower(path), ".blade.php") {
		return "blade"
	}
	for language, extensions := range map[string][]string{
		"python": {".py", ".pyi"}, "typescript": {".ts", ".mts", ".cts"}, "typescriptreact": {".tsx"}, "javascript": {".js", ".mjs", ".cjs"}, "javascriptreact": {".jsx"}, "vue": {".vue"}, "svelte": {".svelte"}, "astro": {".astro"}, "go": {".go"}, "rust": {".rs"}, "c": {".c", ".h"}, "cpp": {".cpp", ".cc", ".cxx", ".hh", ".hpp", ".hxx"}, "yaml": {".yaml", ".yml"}, "lua": {".lua"}, "php": {".php"}, "ocaml": {".ml", ".mli"}, "shellscript": {".sh", ".bash", ".zsh", ".ksh"}, "terraform": {".tf", ".tfvars"}, "dart": {".dart"}, "haskell": {".hs", ".lhs"}, "julia": {".jl"}, "clojure": {".clj", ".cljc", ".edn"}, "clojurescript": {".cljs"}, "nix": {".nix"}, "zig": {".zig", ".zon"}, "gleam": {".gleam"}, "elixir": {".ex", ".exs"}, "prisma": {".prisma"}, "kotlin": {".kt", ".kts"}, "java": {".java"}, "powershell": {".ps1", ".psm1", ".psd1"}, "dockerfile": {".dockerfile"},
	} {
		for _, e := range extensions {
			if e == ext {
				return language
			}
		}
	}
	if strings.EqualFold(filepath.Base(path), "Dockerfile") {
		return "dockerfile"
	}
	return "plaintext"
}
