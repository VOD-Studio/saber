// Package desktop 启动隔离的 Electron 客户端。
package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"rua.plus/saber/internal/config"
)

// Command 启动桌面，或显式安装依赖与打包；不会隐式下载程序。
func Command(ctx context.Context, args []string, cfg config.DesktopConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := cfg.AppDir
	if dir == "" {
		cwd, e := os.Getwd()
		if e != nil {
			return e
		}
		dir = filepath.Join(cwd, "desktop")
	}
	if len(args) > 0 && args[0] == "dashboard" {
		return Dashboard(ctx, cfg, dir)
	}
	action := "start"
	if len(args) > 0 {
		switch args[0] {
		case "install":
			action = "install"
		case "build":
			action = "dist"
		case "start":
		default:
			return fmt.Errorf("desktop: 使用 start|install|build")
		}
	}
	argv := []string{"run", action}
	if action == "install" {
		argv = []string{"install"}
	}
	cmd := exec.CommandContext(ctx, "npm", argv...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "SABER_DESKTOP_ENDPOINT="+cfg.Endpoint, "SABER_DESKTOP_TOKEN_FILE="+cfg.TokenFile, "SABER_DESKTOP_STATE="+cfg.StateDir, "SABER_DESKTOP_THEME="+cfg.Theme, "SABER_DESKTOP_LANGUAGE="+cfg.Language, "SABER_DESKTOP_SHORTCUT="+cfg.QuickEntryShortcut)
	cmd.Env = append(cmd.Env, "SABER_DESKTOP_FONT="+cfg.FontFamily, "SABER_DESKTOP_TERMINAL_FONT="+cfg.TerminalFontFamily)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("桌面 %s 失败: %w", action, err)
	}
	return nil
}
