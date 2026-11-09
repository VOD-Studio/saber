package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"rua.plus/saber/internal/config"
)

type managedOperation struct {
	ID         string    `json:"id"`
	Connector  string    `json:"connector"`
	AccountID  string    `json:"account_id,omitempty"`
	Status     string    `json:"status"`
	ConnectURL string    `json:"connect_url,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (c *connectorManager) managedPath(ctx context.Context, name, target string) string {
	sum := sha256.Sum256([]byte(connectorKey(ctx, name) + ":" + target))
	return filepath.Join(c.host.StateDir, "connectors", fmt.Sprintf("managed-%x.json", sum[:16]))
}
func (c *connectorManager) managedRead(ctx context.Context, name, target string) (managedOperation, error) {
	var op managedOperation
	raw, err := os.ReadFile(c.managedPath(ctx, name, target))
	if err != nil {
		return op, err
	}
	if json.Unmarshal(raw, &op) != nil {
		return op, errors.New("managed connection operation invalid")
	}
	return op, nil
}
func (c *connectorManager) managedSave(ctx context.Context, name string, op managedOperation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return writePrivate(c.managedPath(ctx, name, op.Connector), raw)
}
func (c *connectorManager) managedConnect(ctx context.Context, name, target string, cfg config.PluginConnectorConfig, reconnect bool) (any, error) {
	if !slugPattern.MatchString(target) {
		return nil, errors.New("managed connector requires a safe app slug")
	}
	permitted := contains(cfg.AllowedTools, target)
	if !permitted {
		for _, allowed := range cfg.AllowedTools {
			if len(allowed) > len(target) && allowed[:len(target)+1] == target+":" {
				permitted = true
			}
		}
	}
	if !permitted {
		return nil, errors.New("managed app not permitted by configured allowlist")
	}
	op := managedOperation{ID: randomID(), Connector: target, Status: "initiated", CreatedAt: time.Now()}
	body := map[string]any{"connectors": []string{target}, "reinitiate": reconnect, "op": op.ID, "initiator": "agent_tool"}
	if reconnect {
		if previous, err := c.managedRead(ctx, name, target); err == nil && previous.AccountID != "" {
			body["connectionId"] = previous.AccountID
		}
	}
	// 先记录派发意图；重启只读取结果，不重新请求新的 OAuth 链接。
	if err := c.managedSave(ctx, name, op); err != nil {
		return nil, err
	}
	response, err := c.request(ctx, name, cfg, http.MethodPost, "v1/connectors/connections", body)
	if err != nil {
		op.Status = "failed"
		op.Reason = "connect request failed; no automatic replay"
		saveErr := c.managedSave(ctx, name, op)
		return nil, errors.Join(err, saveErr)
	}
	rows, _ := response["results"].([]any)
	found := false
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok || row["connector"] != target {
			continue
		}
		found = true
		op.Status, _ = row["status"].(string)
		op.AccountID, _ = row["connectionId"].(string)
		op.ConnectURL, _ = row["connectUrl"].(string)
		op.Reason, _ = row["statusReason"].(string)
		break
	}
	if !found || !contains([]string{"active", "initiated", "failed"}, op.Status) {
		op.Status = "failed"
		op.Reason = "gateway returned no matching valid connection outcome"
	}
	if op.ConnectURL != "" {
		if err := safeOAuthEndpoint(op.ConnectURL); err != nil {
			op.Status = "failed"
			op.ConnectURL = ""
			op.Reason = "gateway returned unsafe authorization link"
		}
	}
	if err := c.managedSave(ctx, name, op); err != nil {
		return nil, err
	}
	return op, nil
}
func (c *connectorManager) managedContinue(ctx context.Context, name, target string, cfg config.PluginConnectorConfig) (any, error) {
	op, err := c.managedRead(ctx, name, target)
	if err != nil {
		return nil, errors.New("managed operation not found")
	}
	if op.Status != "initiated" && op.Status != "pending" {
		return op, nil
	}
	if op.AccountID == "" {
		op.Status = "interrupted"
		op.Reason = "no account row was recorded; explicitly reconnect instead of replaying connect"
		return op, c.managedSave(ctx, name, op)
	}
	row, err := c.request(ctx, name, cfg, http.MethodGet, "v1/connectors/accounts/"+url.PathEscape(op.AccountID), nil)
	if err != nil {
		return op, err
	}
	if row["connectionId"] != op.AccountID || row["connector"] != target {
		return nil, errors.New("gateway account response is outside operation scope")
	}
	status, _ := row["status"].(string)
	if !contains([]string{"pending", "active", "failed", "expired", "revoked", "inactive"}, status) {
		return nil, errors.New("gateway account status unknown")
	}
	op.Status = status
	op.Reason, _ = row["statusReason"].(string)
	if status != "pending" {
		op.ConnectURL = ""
	}
	return op, c.managedSave(ctx, name, op)
}
func (c *connectorManager) managedCancel(ctx context.Context, name, target string) (any, error) {
	op, err := c.managedRead(ctx, name, target)
	if err != nil {
		return nil, err
	}
	if op.Status == "active" {
		return op, nil
	}
	op.Status = "canceled"
	op.ConnectURL = ""
	op.Reason = "local authorization attempt canceled; existing remote account was not deleted"
	return op, c.managedSave(ctx, name, op)
}
func (c *connectorManager) managedRemove(ctx context.Context, name, target string, cfg config.PluginConnectorConfig) (any, error) {
	op, err := c.managedRead(ctx, name, target)
	if err != nil || op.AccountID == "" {
		return nil, errors.New("managed account is not owned by a recorded operation in this user scope")
	}
	result, err := c.request(ctx, name, cfg, http.MethodDelete, "v1/connectors/accounts/"+url.PathEscape(op.AccountID), nil)
	if err != nil {
		return nil, err
	}
	op.Status = "removed"
	op.ConnectURL = ""
	if err := c.managedSave(ctx, name, op); err != nil {
		return nil, err
	}
	return result, nil
}
func (c *connectorManager) filterManagedSearch(ctx context.Context, name string, cfg config.PluginConnectorConfig, result map[string]any) map[string]any {
	out := map[string]any{}
	rows, _ := result["results"].([]any)
	groups := []any{}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		group := map[string]any{"index": row["index"], "useCase": row["useCase"]}
		for _, key := range []string{"tools", "relatedTools"} {
			tools, _ := row[key].([]any)
			kept := []any{}
			for _, v := range tools {
				tool, ok := v.(string)
				if ok {
					if _, err := c.authorize(ctx, name, tool); err == nil {
						kept = append(kept, tool)
					}
				}
			}
			group[key] = kept
		}
		groups = append(groups, group)
	}
	out["results"] = groups
	return out
}
