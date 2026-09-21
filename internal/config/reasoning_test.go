package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAIConfig_ReasoningEffortInheritance(t *testing.T) {
	var cfg AIConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
default_model: inherited.default
reasoning_effort: low
providers:
  relay:
    reasoning_effort: medium
    models:
      default: {model: default}
      strong: {model: strong, reasoning_effort: high}
      off: {model: off, reasoning_effort: none}
  inherited:
    models:
      default: {model: default}
models:
  alias: {provider: relay, model: default}
  alias_override: {provider: relay, model: default, reasoning_effort: xhigh}
  default_provider_alias: {model: old}
  default_provider_override: {model: old, reasoning_effort: max}
`), &cfg))
	for _, tc := range []struct{ id, want string }{
		{"relay.default", "medium"}, {"relay.unlisted", "medium"}, {"relay.strong", "high"}, {"relay.off", "none"},
		{"inherited.default", "low"}, {"inherited.unlisted", "low"}, {"alias", "medium"}, {"alias_override", "xhigh"},
		{"default_provider_alias", "low"}, {"default_provider_override", "max"}, {"inherited.unlisted", "low"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, _ := cfg.GetModelConfig(tc.id)
			require.Equal(t, tc.want, got.ReasoningEffort)
		})
	}
	cfg = AIConfig{}
	got, _ := cfg.GetModelConfig("unlisted")
	require.Empty(t, got.ReasoningEffort)
}
