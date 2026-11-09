package execution

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"rua.plus/saber/internal/config"
)

func validateRemoteConfig(cfg *config.ExecutionConfig) error {
	cfg.Extensions = maps.Clone(cfg.Extensions)
	for name, extension := range cfg.Extensions {
		if !featureName.MatchString(name) || slices.Contains([]string{"local", "docker", "ssh", "modal", "daytona", "vercel"}, name) {
			return fmt.Errorf("invalid or reserved execution backend name %q", name)
		}
		if len(extension.Command) == 0 || extension.Command[0] == "" {
			return fmt.Errorf("execution extension %q requires command", name)
		}
		extension.Command = slices.Clone(extension.Command)
		extension.Env = maps.Clone(extension.Env)
		for _, arg := range extension.Command {
			if strings.ContainsRune(arg, 0) {
				return errors.New("execution adapter command contains NUL")
			}
		}
		if err := ValidateEnvironment(extension.Env, nil); err != nil {
			return err
		}
		cfg.Extensions[name] = extension
	}
	if cfg.SSH.Port == 0 {
		cfg.SSH.Port = 22
	}
	if cfg.SSH.RemoteRoot == "" {
		cfg.SSH.RemoteRoot = "/tmp"
	}
	if cfg.Remote.Python == "" {
		cfg.Remote.Python = "python3"
	}
	if cfg.Remote.CPU == 0 {
		cfg.Remote.CPU = 1
	}
	if cfg.Remote.MemoryMB == 0 {
		cfg.Remote.MemoryMB = 512
	}
	if cfg.Remote.DiskMB == 0 {
		cfg.Remote.DiskMB = 10240
	}
	if cfg.Remote.LifetimeSeconds == 0 {
		cfg.Remote.LifetimeSeconds = 3600
	}
	if cfg.Remote.CPU < 1 || cfg.Remote.CPU > 64 || cfg.Remote.MemoryMB < 128 || cfg.Remote.MemoryMB > 262144 || cfg.Remote.DiskMB < 1024 || cfg.Remote.LifetimeSeconds < 60 || cfg.Remote.LifetimeSeconds > 86400 {
		return errors.New("invalid execution remote resource limits")
	}
	cfg.Remote.Credentials = maps.Clone(cfg.Remote.Credentials)
	allowed := []string{"MODAL_TOKEN_ID", "MODAL_TOKEN_SECRET", "DAYTONA_API_KEY", "DAYTONA_API_URL", "DAYTONA_TARGET", "VERCEL_TOKEN", "VERCEL_PROJECT_ID", "VERCEL_TEAM_ID", "VERCEL_OIDC_TOKEN"}
	for key, value := range cfg.Remote.Credentials {
		if !slices.Contains(allowed, key) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("unsupported remote SDK credential %q", key)
		}
	}
	for name := range cfg.Workspaces {
		backend, err := cfg.BackendFor(name)
		if err != nil {
			return err
		}
		if backend == "ssh" {
			ssh := cfg.SSH
			if ssh.Host == "" || ssh.User == "" || strings.ContainsAny(ssh.Host+ssh.User, " \t\r\n;\x00") || strings.HasPrefix(ssh.Host, "-") || strings.HasPrefix(ssh.User, "-") || ssh.Port < 1 || ssh.Port > 65535 || ssh.KnownHosts == "" {
				return errors.New("execution.ssh requires host, user, valid port and known_hosts")
			}
			if !path.IsAbs(ssh.RemoteRoot) || path.Clean(ssh.RemoteRoot) != ssh.RemoteRoot || ssh.RemoteRoot == "/" {
				return errors.New("execution.ssh.remote_root must be a specific absolute directory")
			}
		}
		if !cfg.IsNetworkEnabled() {
			if backend == "ssh" || backend == "daytona" || backend == "vercel" {
				return fmt.Errorf("%s backend cannot enforce network isolation with the configured SDK", backend)
			}
			if extension, ok := cfg.Extensions[backend]; ok && !extension.NetworkIsolation {
				return fmt.Errorf("extension %q does not enforce network isolation", backend)
			}
		}
	}
	return nil
}
