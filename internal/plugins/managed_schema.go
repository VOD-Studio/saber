package plugins

import (
	"context"
	"errors"
	"net/http"

	"rua.plus/saber/internal/config"
)

func (c *connectorManager) managedSchema(ctx context.Context, name, tool string, cfg config.PluginConnectorConfig) (map[string]any, error) {
	response, err := c.request(ctx, name, cfg, http.MethodPost, "v1/connectors/schemas", map[string]any{"tools": []string{tool}})
	if err != nil {
		return nil, err
	}
	schemas, ok := response["schemas"].(map[string]any)
	if !ok {
		return nil, errors.New("managed connector schemas response invalid")
	}
	schema, ok := schemas[tool].(map[string]any)
	if !ok {
		return nil, errors.New("managed tool schema not found")
	}
	if schema["connector"] == "" || schema["tool"] == "" {
		return nil, errors.New("managed schema missing app/tool identity")
	}
	return schema, nil
}
