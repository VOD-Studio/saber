package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ComputerConfig 固定真实 cua-driver 的位置和不可由模型更改的权限模式。
type ComputerConfig struct {
	Enabled                   bool              `yaml:"enabled"`
	Command                   string            `yaml:"command"`
	Args                      []string          `yaml:"args"`
	Endpoint                  string            `yaml:"endpoint"`
	Headers                   map[string]string `yaml:"headers"`
	AllowHostProcess          bool              `yaml:"allow_host_process"`
	PermissionMode            string            `yaml:"permission_mode"`
	CapabilityManifest        string            `yaml:"capability_manifest"`
	ApproveCapabilityManifest bool              `yaml:"approve_capability_manifest"`
	AllowUnrestricted         bool              `yaml:"allow_unrestricted"`
	AllowUnsignedDriver       bool              `yaml:"allow_unsigned_driver"`
	AllowForeground           bool              `yaml:"allow_foreground"`
	NoOverlay                 *bool             `yaml:"no_overlay"`
	Telemetry                 bool              `yaml:"cua_telemetry"`
	AXMaxElements             int               `yaml:"ax_max_elements"`
	MaxImageDimension         int               `yaml:"max_image_dimension"`
	CommandTimeout            int               `yaml:"command_timeout"`
	InactivityTimeout         int               `yaml:"inactivity_timeout"`
	MaxSessions               int               `yaml:"max_sessions"`
}

// DefaultComputerConfig 仅在显式启用和授权后连接标准模式的真实驱动。
func DefaultComputerConfig() ComputerConfig {
	return ComputerConfig{Command: "cua-driver", PermissionMode: "standard", AXMaxElements: 200, MaxImageDimension: 1456, CommandTimeout: 30, InactivityTimeout: 120, MaxSessions: 8}
}

// Validate 不降级权限模式，不把容器调用偷偷改成宿主桌面调用。
func (c ComputerConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Endpoint == "" && !c.AllowHostProcess {
		return fmt.Errorf("computer_use.allow_host_process must explicitly permit the host display; otherwise configure a sandbox MCP endpoint")
	}
	if c.Endpoint != "" {
		u, e := url.Parse(c.Endpoint)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("computer_use.endpoint must be a trusted HTTP(S) MCP endpoint")
		}
	}
	switch c.PermissionMode {
	case "standard":
	case "bounded":
		if c.CapabilityManifest == "" || !c.ApproveCapabilityManifest {
			return fmt.Errorf("computer_use bounded mode requires a reviewed capability_manifest and approve_capability_manifest=true")
		}
	case "unrestricted":
		if !c.AllowUnrestricted {
			return fmt.Errorf("computer_use unrestricted mode requires explicit allow_unrestricted=true risk acceptance")
		}
	default:
		return fmt.Errorf("computer_use.permission_mode: expected standard, bounded or unrestricted")
	}
	if c.Endpoint != "" && c.PermissionMode != "standard" {
		return fmt.Errorf("computer_use remote runtime permission mode is owned by its server; use standard locally and configure remote server grants")
	}
	if c.CommandTimeout < 1 || c.CommandTimeout > 1800 || c.InactivityTimeout < 1 || c.MaxSessions < 1 || c.MaxSessions > 128 || c.AXMaxElements < 0 || c.MaxImageDimension < 0 {
		return fmt.Errorf("computer_use invalid timeout/session/image/tree limits")
	}
	for _, arg := range c.Args {
		for _, flag := range []string{"--permission-mode", "--dangerously-bypass-approvals", "--capability-manifest", "--approve-capability-manifest", "--socket", "--embedded", "--no-permissions-gate"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return fmt.Errorf("computer_use.args cannot override trusted runtime mode or lifecycle: %s", flag)
			}
		}
	}
	return nil
}
