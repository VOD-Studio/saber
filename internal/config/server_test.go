package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPServerConfig_LocalOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8320", "[::1]:8320", "localhost:0"} {
		require.NoError(t, (HTTPServerConfig{Listen: addr}).Validate())
	}
	for _, addr := range []string{"0.0.0.0:8320", ":8320", "10.0.0.1:8320", "invalid"} {
		require.Error(t, (HTTPServerConfig{Listen: addr}).Validate())
	}
	require.Equal(t, filepath.Join("/tmp", ".saber-token"), (HTTPServerConfig{}).TokenPath("/tmp/config.yaml"))
}
