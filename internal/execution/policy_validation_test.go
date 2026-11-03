package execution

import (
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/config"
)

// TestValidateConfig_RejectsUnenforcedAuthorization 验证未实施的能力与 local 后端通配授权在启动时被拒绝。
func TestValidateConfig_RejectsUnenforcedAuthorization(t *testing.T) {
	workspace := t.TempDir()
	for _, tc := range []struct {
		name    string
		mutate  func(*config.ExecutionConfig)
		wantErr string
	}{
		{
			name:    "wildcard room on local",
			mutate:  func(c *config.ExecutionConfig) { c.Grants[0].Room = "*" },
			wantErr: `workspace "project" cannot grant wildcard room/users on the local backend: local execution is not a sandbox; use backend: docker`,
		},
		{
			name:    "wildcard users on local",
			mutate:  func(c *config.ExecutionConfig) { c.Grants[0].Users = []string{"*"} },
			wantErr: "local execution is not a sandbox",
		},
		{
			name: "wildcard room on docker",
			mutate: func(c *config.ExecutionConfig) {
				c.Backend = "docker"
				c.Grants[0].Room = "*"
			},
		},
		{
			name: "wildcard users on docker",
			mutate: func(c *config.ExecutionConfig) {
				c.Backend = "docker"
				c.Grants[0].Users = []string{"*"}
			},
		},
		{
			name:    "publish capability",
			mutate:  func(c *config.ExecutionConfig) { c.Grants[0].Capabilities = []string{"publish"} },
			wantErr: `capability "publish" is not enforced by this build (only external is supported)`,
		},
		{
			name: "publish mcp requirement",
			mutate: func(c *config.ExecutionConfig) {
				c.Grants[0].Tools = append(c.Grants[0].Tools, "mcp:release:publish")
				c.MCPRequirements = map[string][]string{"mcp:release:publish": {"publish"}}
			},
			wantErr: "is not enforced by this build",
		},
		{
			name:   "external capability",
			mutate: func(c *config.ExecutionConfig) { c.Grants[0].Capabilities = []string{"external"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.ExecutionConfig{
				LogDir:     t.TempDir(),
				Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workspace}},
				Grants: []config.ExecutionGrant{{
					Platform: "matrix", Account: "bot", Room: "room", Users: []string{"alice"},
					Workspace: "project", Tools: []string{"exec"},
				}},
			}
			tc.mutate(&cfg)
			_, err := ValidateConfig(cfg, nil, nil)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
