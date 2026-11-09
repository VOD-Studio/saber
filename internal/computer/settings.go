package computer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (d *driver) setConfig(ctx context.Context, key string, value any) error {
	var args map[string]any
	if d.property("set_config", "key") && d.property("set_config", "value") {
		args = map[string]any{"key": key, "value": value}
	} else if d.property("set_config", key) {
		args = map[string]any{key: value}
	} else if d.property("set_config", "config") {
		args = map[string]any{"config": map[string]any{key: value}}
	} else {
		return fmt.Errorf("live cua-driver does not advertise setting %s", key)
	}
	_, err := d.call(ctx, "set_config", args)
	return err
}
func (d *driver) captureDesktop(ctx context.Context) (out *mcp.CallToolResult, err error) {
	cfg, err := d.call(ctx, "get_config", map[string]any{})
	if err != nil {
		return nil, err
	}
	previous, _ := resultObject(cfg)["capture_scope"].(string)
	if previous == "" {
		return nil, errors.New("driver get_config did not report capture_scope; cannot safely change and restore desktop capture")
	}
	if previous != "desktop" {
		if err = d.setConfig(ctx, "capture_scope", "desktop"); err != nil {
			return nil, err
		}
		defer func() {
			restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			err = errors.Join(err, d.setConfig(restoreCtx, "capture_scope", previous))
		}()
	}
	return d.call(ctx, "get_desktop_state", map[string]any{})
}
