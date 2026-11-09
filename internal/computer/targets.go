package computer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func appRows(out *mcp.CallToolResult) []any {
	if rows, ok := out.StructuredContent.([]any); ok {
		return rows
	}
	if data := resultObject(out); data != nil {
		for _, key := range []string{"apps", "applications"} {
			if rows, ok := data[key].([]any); ok {
				return rows
			}
		}
	}
	var rows []any
	_ = json.Unmarshal([]byte(resultText(out)), &rows)
	return rows
}
func pickWindow(ctx context.Context, b *computerSession, app string) (int, int, error) {
	out, err := b.driver.call(ctx, "list_windows", map[string]any{"on_screen_only": true})
	if err != nil {
		return 0, 0, err
	}
	rows := windows(out)
	if strings.EqualFold(app, "desktop") {
		var backdrop, other []map[string]any
		for _, row := range rows {
			name := strings.ToLower(stringArg(row, "app_name") + " " + stringArg(row, "title"))
			if strings.Contains(name, "progman") || strings.Contains(name, "workerw") || strings.Contains(name, "program manager") || strings.Contains(name, "finder") || strings.Contains(name, "desktop") {
				backdrop = append(backdrop, row)
			} else if strings.Contains(name, "shell_traywnd") || strings.Contains(name, "taskbar") || strings.Contains(name, "dock") {
				other = append(other, row)
			}
		}
		rows = append(backdrop, other...)
	} else if app != "" {
		var exact, partial []map[string]any
		for _, row := range rows {
			name := stringArg(row, "app_name")
			if strings.EqualFold(name, app) {
				exact = append(exact, row)
			} else if strings.Contains(strings.ToLower(name), strings.ToLower(app)) {
				partial = append(partial, row)
			}
		}
		if len(exact) == 0 && b.driver.tools["list_apps"] != nil {
			apps, err := b.driver.call(ctx, "list_apps", map[string]any{})
			if err != nil {
				return 0, 0, err
			}
			pids := make(map[int]bool)
			for _, raw := range appRows(apps) {
				a, ok := raw.(map[string]any)
				if !ok || a["running"] == false {
					continue
				}
				for _, key := range []string{"bundle_id", "bundleId", "name", "app_name", "display_name"} {
					if strings.EqualFold(stringArg(a, key), app) {
						if pid, ok := integer(a["pid"]); ok {
							pids[pid] = true
						}
					}
				}
			}
			for _, row := range rows {
				pid, _ := integer(row["pid"])
				if pids[pid] {
					exact = append(exact, row)
				}
			}
		}
		if len(exact) > 0 {
			rows = exact
		} else {
			rows = partial
		}
	} else {
		var real []map[string]any
		for _, row := range rows {
			name := strings.ToLower(stringArg(row, "app_name"))
			title := strings.ToLower(stringArg(row, "title"))
			if strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), " ", "") == "cuadriver" || strings.HasPrefix(title, "@!") || strings.HasPrefix(title, "desktop") || strings.HasPrefix(title, "gnome-shell") {
				continue
			}
			real = append(real, row)
		}
		if len(real) > 0 {
			rows = real
		}
	}
	if len(rows) == 0 {
		return 0, 0, errors.New("no matching visible window; use list_windows/list_apps or doctor for display, permissions and locked-session diagnosis")
	}
	pid, _ := integer(rows[0]["pid"])
	window, _ := integer(rows[0]["window_id"])
	return pid, window, nil
}
