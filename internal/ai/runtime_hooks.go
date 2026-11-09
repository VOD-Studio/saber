package ai

import (
	"context"
	"encoding/json"
	"log/slog"

	"rua.plus/saber/internal/agent"
)

func (s *Service) finishRuntimeTurn(ctx context.Context, req agent.Request, result agent.Result, runErr error) error {
	if s.automationService != nil {
		if hookErr := s.automationService.AfterTurn(ctx, result, runErr); hookErr != nil {
			slog.Error("任务结束通知失败", "error", hookErr)
		}
	}
	return s.SyncExternalMemory(ctx, req, result)
}

func (s *Service) runtimeNotifications(ctx context.Context, wait bool) ([]string, error) {
	if s.executor == nil || isPublicThread(ctx) {
		return nil, nil
	}
	var notices []string
	if agent.ToolAllowed(ctx, "process") && s.executor.Check(ctx, "process") == nil {
		events, err := s.executor.DrainProcessNotifications(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			encoded, err := json.Marshal(event)
			if err != nil {
				return nil, err
			}
			notices = append(notices, "进程状态: "+string(encoded))
		}
	}
	if agent.ToolAllowed(ctx, "read_file") && s.executor.Check(ctx, "read_file") == nil {
		diagnostics, err := s.executor.DrainTaskDiagnostics(ctx, wait)
		if err != nil {
			return notices, err
		}
		for _, diagnostic := range diagnostics {
			encoded, err := json.Marshal(diagnostic)
			if err != nil {
				return notices, err
			}
			notices = append(notices, "文件语义诊断: "+string(encoded))
		}
	}
	return notices, nil
}
