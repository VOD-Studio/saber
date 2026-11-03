package bot

import (
	"os"
	"path/filepath"

	"rua.plus/saber/internal/config"
)

// resolveConfigPath 跟随配置文件符号链接，确定真实配置目录；不创建目标。
func resolveConfigPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	return filepath.EvalSymlinks(path)
}

// executionInputs 构建执行器与 stdio MCP 共用的保护路径和机器人凭据列表。
func executionInputs(cfg *config.Config, configPath string) (protectedPaths, forbiddenSecrets []string) {
	configDir := filepath.Dir(configPath)
	protectedPaths = []string{
		cfg.Server.TokenPath(configPath),
		configPath,
		configPath + ".session",
		filepath.Join(configDir, "tasks.db"),
		filepath.Join(configDir, "persona.db"),
		filepath.Join(configDir, "memory.db"),
	}
	if cfg.Execution.LogDir != "" {
		logDir := cfg.Execution.LogDir
		if !filepath.IsAbs(logDir) {
			logDir = filepath.Join(configDir, logDir)
		}
		protectedPaths = append(protectedPaths, logDir)
	}
	if cfg.Platforms.Matrix.Enabled {
		protectedPaths = append(protectedPaths, cfg.Matrix.E2EESessionPath, cfg.Matrix.E2EESessionPath+".key", cfg.Matrix.PickleKeyPath)
	}
	forbiddenSecrets = []string{cfg.Matrix.AccessToken, cfg.Matrix.Password}
	for _, provider := range cfg.AI.Providers {
		forbiddenSecrets = append(forbiddenSecrets, provider.APIKey)
		for _, model := range provider.Models {
			forbiddenSecrets = append(forbiddenSecrets, model.APIKey)
		}
	}
	for _, model := range cfg.AI.Models {
		forbiddenSecrets = append(forbiddenSecrets, model.APIKey)
	}
	return protectedPaths, forbiddenSecrets
}
