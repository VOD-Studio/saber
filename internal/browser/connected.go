package browser

import (
	"context"
	"errors"
)

func (b *browserSession) attachTab(ctx context.Context, target string, owned bool) error {
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.cdp.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &attached); err != nil {
		return err
	}
	b.mu.Lock()
	b.tabs[target] = attached.SessionID
	b.ownedTabs[target] = owned
	b.current = target
	b.mu.Unlock()
	for _, item := range []struct {
		method string
		params any
	}{{"Page.enable", map[string]any{}}, {"Runtime.enable", map[string]any{}}, {"Network.enable", map[string]any{}}, {"Network.setBypassServiceWorker", map[string]any{"bypass": true}}, {"Fetch.enable", map[string]any{"patterns": []any{map[string]any{"urlPattern": "*", "requestStage": "Request"}}}}, {"Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}}} {
		if err := b.cdp.call(ctx, attached.SessionID, item.method, item.params, nil); err != nil {
			return err
		}
	}
	return b.installCloudDialogs(ctx, attached.SessionID)
}
func (b *browserSession) attachExisting(ctx context.Context) error {
	var out struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := b.cdp.call(ctx, "", "Target.getTargets", map[string]any{}, &out); err != nil {
		return err
	}
	first := ""
	for _, target := range out.TargetInfos {
		if target.Type != "page" || b.checkURL(ctx, target.URL) != nil {
			continue
		}
		if err := b.attachTab(ctx, target.TargetID, false); err != nil {
			return err
		}
		if first == "" {
			first = target.TargetID
		}
	}
	if first == "" {
		return errors.New("no policy-allowed connected tabs; disable attach_existing_tabs to create an isolated context")
	}
	b.mu.Lock()
	b.current = first
	b.mu.Unlock()
	return nil
}
