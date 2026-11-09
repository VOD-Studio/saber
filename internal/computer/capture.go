package computer

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Service) captureWindow(ctx context.Context, b *computerSession, pid, window int, mode string) (*mcp.CallToolResult, error) {
	if mode == "vision" && b.driver.tools["screenshot"] != nil {
		return b.driver.call(ctx, "screenshot", map[string]any{"window_id": window, "format": "jpeg", "quality": 85})
	}
	params := map[string]any{"pid": pid, "window_id": window}
	if s.cfg.AXMaxElements > 0 && b.driver.property("get_window_state", "max_elements") {
		params["max_elements"] = s.cfg.AXMaxElements
	}
	return b.driver.call(ctx, "get_window_state", params)
}
