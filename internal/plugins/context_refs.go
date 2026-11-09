package plugins

import (
	"context"
	"errors"
	"strings"
)

// ContextReferences 为每次引用解析提供实时插件集合，不允许插件替换内置前缀。
func (m *Manager) ContextReferences(ctx context.Context) map[string]func(context.Context, string, int) (string, error) {
	m.mu.RLock()
	names := []string{}
	for name, process := range m.running {
		if contains(process.manifest.Providers["context_refs"], "expand") {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()
	out := map[string]func(context.Context, string, int) (string, error){}
	for _, name := range names {
		if _, err := m.authorizeRunningPlugin(ctx, name); err != nil {
			continue
		}
		out["plugin-"+name] = func(call context.Context, target string, maxBytes int) (string, error) {
			if maxBytes < 1 || maxBytes > 4<<20 {
				return "", errors.New("plugin context reference byte limit out of range")
			}
			result, err := m.ProviderCall(call, name, "context_refs", "expand", map[string]any{"target": target, "max_bytes": maxBytes})
			if err != nil {
				return "", err
			}
			object, ok := result.(map[string]any)
			if !ok {
				return "", errors.New("context reference provider result must be object")
			}
			text, ok := object["text"].(string)
			if !ok {
				return "", errors.New("context reference provider missing text")
			}
			if len(text) > maxBytes {
				return "", errors.New("context reference provider exceeded byte limit")
			}
			return text, nil
		}
	}
	return out
}

// ContextReferenceAutocomplete 仅调用同一已启用插件声明的补全操作。
func (m *Manager) ContextReferenceAutocomplete(ctx context.Context, prefix, target string, max int) ([]string, error) {
	if !strings.HasPrefix(prefix, "plugin-") || max < 1 || max > 100 {
		return nil, errors.New("invalid plugin reference autocomplete")
	}
	result, err := m.ProviderCall(ctx, strings.TrimPrefix(prefix, "plugin-"), "context_refs", "autocomplete", map[string]any{"target": target, "max_items": max})
	if err != nil {
		return nil, err
	}
	object, ok := result.(map[string]any)
	if !ok {
		return nil, errors.New("context autocomplete result must be object")
	}
	raw, ok := object["items"].([]any)
	if !ok || len(raw) > max {
		return nil, errors.New("context autocomplete result exceeds schema/limit")
	}
	items := make([]string, len(raw))
	for i, item := range raw {
		value, ok := item.(string)
		if !ok {
			return nil, errors.New("context autocomplete item must be string")
		}
		items[i] = value
	}
	return items, nil
}
