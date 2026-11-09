package server

import (
	"fmt"
	"net/http"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/task"
)

func responseEvent(w http.ResponseWriter, c *http.ResponseController, t task.Task, record task.Record) error {
	if record.Text != "" && record.Kind == agent.TextDelta {
		kind := "response.output_text.delta"
		return sendEvent(w, c, kind, record.ID, map[string]any{"type": kind, "response_id": fmt.Sprintf("resp_%d", t.ID), "item_id": fmt.Sprintf("msg_%d", t.ID), "output_index": 0, "content_index": 0, "delta": record.Text})
	}
	if record.Tool.Call.ID != "" {
		item := map[string]any{"type": "function_call", "id": record.Tool.Call.ID, "call_id": record.Tool.Call.ID, "name": record.Tool.Call.Function.Name, "arguments": record.Tool.Call.Function.Arguments, "status": "in_progress"}
		kind := "response.output_item.added"
		if record.Kind == agent.ToolFinished {
			kind = "response.output_item.done"
			item["status"] = "completed"
			item["output"] = record.Tool.Content
			if record.Tool.ErrorCode != "" {
				item["status"] = "failed"
				item["error"] = record.Tool.ErrorCode
			}
		}
		return sendEvent(w, c, kind, record.ID, map[string]any{"type": kind, "response_id": fmt.Sprintf("resp_%d", t.ID), "output_index": record.Round, "item": item})
	}
	return sendEvent(w, c, "response.agent_event", record.ID, record)
}
