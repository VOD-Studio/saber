package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"rua.plus/saber/internal/config"
)

func (c *connectorManager) probeManaged(ctx context.Context, cfg config.PluginConnectorConfig, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.URL, "/")+"/v1/connectors", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("managed connector rejected candidate authorization")
	}
	var result struct {
		Items []json.RawMessage `json:"items"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil || result.Items == nil {
		return errors.New("managed connector probe returned invalid account catalog")
	}
	return nil
}
