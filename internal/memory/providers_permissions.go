package memory

import (
	"context"
	"strings"

	"rua.plus/saber/internal/chat"
)

func providerToolReadOnly(name string, args map[string]any) bool {
	switch name {
	case "mem0_search", "honcho_search", "honcho_context", "honcho_reasoning", "viking_search", "viking_rule_search", "viking_read", "viking_browse", "hindsight_recall", "hindsight_reflect", "supermemory_search", "supermemory_profile", "memori_recall", "memori_recall_summary", "memori_quota":
		return true
	case "honcho_profile":
		_, write := args["card"]
		return !write
	case "honcho_conclude":
		list, _ := args["list"].(bool)
		return list && args["conclusion"] == nil && args["delete_id"] == nil
	case "fact_store":
		action, _ := args["action"].(string)
		switch action {
		case "search", "probe", "related", "reason", "contradict", "list":
			return true
		}
	case "brv_query", "brv_status", "retaindb_profile", "retaindb_search", "retaindb_context", "retaindb_list_files", "retaindb_read_file":
		return true
	}
	return false
}

func providerOperationWrites(operation string) bool {
	switch operation {
	case "sync_turn", "pre_compress", "delegation", "memory_write", "session_end", "shutdown":
		return true
	}
	return false
}

// ExternalMemoryWritesAllowed 保留群共享空间的记忆管理员边界，普通成员的自动同步不会使外部事实直接生效。
func (s *Service) ExternalMemoryWritesAllowed(ctx context.Context) bool {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return false
	}
	scope, err := space(identity)
	return err == nil && s.writeAllowed(identity, scope)
}

// ExternalCheckpointStrict 判断是否要求外部耐久检查点；非严格失败仍保留真实状态和日志。
func (s *Service) ExternalCheckpointStrict() bool {
	s.providers.mu.Lock()
	defer s.providers.mu.Unlock()
	return s.providers.cfg.StrictCheckpoint
}

func (s *Service) nativeProviderSelected() bool {
	s.providers.mu.Lock()
	defer s.providers.mu.Unlock()
	return strings.HasPrefix(s.providers.cfg.Providers[s.providers.cfg.Active].Plugin, "native:")
}
