package devtools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var serverMarkers = map[string][]string{
	"pyright":             {"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile", "pyrightconfig.json"},
	"typescript":          {"package-lock.json", "bun.lockb", "bun.lock", "pnpm-lock.yaml", "yarn.lock", "package.json", "tsconfig.json"},
	"vue-language-server": {"package.json", "tsconfig.json"}, "svelte-language-server": {"package.json", "tsconfig.json"}, "astro-language-server": {"package.json", "tsconfig.json"},
	"gopls": {"go.work", "go.mod", "go.sum"}, "rust-analyzer": {"Cargo.toml", "Cargo.lock"}, "clangd": {"compile_commands.json", "compile_flags.txt", ".clangd"},
	"lua-language-server": {".luarc.json", ".luarc.jsonc", ".luacheckrc", ".stylua.toml", "stylua.toml", "selene.toml", "selene.yml"},
	"laravel-lsp":         {"artisan", "composer.json", "composer.lock"}, "intelephense": {"composer.json", "composer.lock", ".php-version"},
	"ocaml-lsp": {"dune-project", "dune-workspace", ".merlin", "opam"}, "terraform-ls": {".terraform.lock.hcl", "terraform.tfstate"},
	"dart": {"pubspec.yaml", "analysis_options.yaml"}, "haskell-language-server": {"stack.yaml", "cabal.project", "hie.yaml"}, "julia": {"Project.toml", "Manifest.toml"},
	"clojure-lsp": {"deps.edn", "project.clj", "shadow-cljs.edn", "bb.edn", "build.boot"}, "nixd": {"flake.nix"}, "zls": {"build.zig"}, "gleam": {"gleam.toml"},
	"elixir-ls": {"mix.exs", "mix.lock"}, "prisma": {"schema.prisma", "prisma/schema.prisma"}, "kotlin-language-server": {"settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts", "pom.xml"},
	"jdtls": {"pom.xml", "build.gradle", "build.gradle.kts", ".project", ".classpath", "settings.gradle"}, "powershell": {"PSScriptAnalyzerSettings.psd1"},
}

func (m *Manager) projectRoot(ctx context.Context, root, path string, server Server) (string, error) {
	access, ok := m.backend.(interface {
		DeveloperExists(context.Context, string) (bool, error)
	})
	if !ok {
		return root, nil
	}
	for directory, depth := filepath.Dir(path), 0; inside(directory, root) && depth < 64; directory, depth = filepath.Dir(directory), depth+1 {
		if server.ID == "typescript" || server.ID == "vue-language-server" || server.ID == "svelte-language-server" || server.ID == "astro-language-server" {
			for _, marker := range []string{"deno.json", "deno.jsonc"} {
				exists, err := access.DeveloperExists(ctx, filepath.Join(directory, marker))
				if err != nil {
					return "", err
				}
				if exists {
					return "", errors.New("deno workspace requires a configured Deno language server, not TypeScript/Vue/Svelte/ Astro project execution")
				}
			}
		}
		for _, marker := range server.RootMarkers {
			exists, err := access.DeveloperExists(ctx, filepath.Join(directory, marker))
			if err != nil {
				return "", err
			}
			if exists {
				return directory, nil
			}
		}
		if directory == root {
			break
		}
	}
	return root, nil
}
func mergeOptions(base, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		if old, ok := out[key].(map[string]any); ok {
			if next, ok := value.(map[string]any); ok {
				out[key] = mergeOptions(old, next)
				continue
			}
		}
		out[key] = value
	}
	return out
}
func (m *Manager) prepareServer(ctx context.Context, root, project string, server Server) (Server, error) {
	if server.ID != "vue-language-server" {
		return server, nil
	}
	if options, ok := server.InitializationOptions["typescript"].(map[string]any); ok {
		if sdk, ok := options["tsdk"].(string); ok && sdk != "" {
			return server, nil
		}
	}
	sdk := ""
	if access, ok := m.backend.(interface {
		DeveloperExists(context.Context, string) (bool, error)
	}); ok {
		candidate := filepath.Join(project, "node_modules", "typescript", "lib")
		exists, err := access.DeveloperExists(ctx, filepath.Join(candidate, "typescript.js"))
		if err != nil {
			return server, err
		}
		if exists {
			cwd, err := m.runProcess(ctx, root, []string{"node", "-e", "process.stdout.write(process.cwd())"}, nil, 4096)
			if err != nil {
				return server, err
			}
			relative, err := filepath.Rel(root, candidate)
			if err != nil {
				return server, err
			}
			sdk = filepath.Join(strings.TrimSpace(string(cwd)), relative)
		}
	}
	if sdk == "" {
		output, err := m.runProcess(ctx, root, []string{"npm", "root", "--global"}, nil, 4096)
		if err != nil {
			return server, fmt.Errorf("vue LSP requires TypeScript 6 SDK; install typescript@6 in the backend or configure servers.vue-language-server.initialization_options.typescript.tsdk: %w", err)
		}
		sdk = filepath.Join(strings.TrimSpace(string(output)), "typescript", "lib")
	}
	// npm root 的结果是后端路径；校验实际 SDK 存在，不把安装目录名称当作成功证明。
	if _, err := m.runProcess(ctx, root, []string{"node", "-e", "require('fs').accessSync(process.argv[1] + '/typescript.js')", sdk}, nil, 4096); err != nil {
		return server, fmt.Errorf("vue TypeScript SDK unavailable at %s: %w", sdk, err)
	}
	server.InitializationOptions = mergeOptions(server.InitializationOptions, map[string]any{"typescript": map[string]any{"tsdk": sdk}, "vue": map[string]any{"hybridMode": false}})
	return server, nil
}
