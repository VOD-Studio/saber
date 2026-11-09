package server

import (
	"net/http"
	"strings"

	"rua.plus/saber/internal/chat"
)

func (h *handler) runControl(w http.ResponseWriter, r *http.Request) {
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	var args map[string]any
	if err = decodeAPI(w, r, &args); err != nil || args == nil {
		http.Error(w, "需要 JSON 参数对象", http.StatusBadRequest)
		return
	}
	args["task_id"] = id
	ctx := chat.WithIdentity(r.Context(), identity(r))
	var out any
	switch {
	case strings.HasSuffix(r.URL.Path, "clarify"):
		args["action"] = "answer"
		out, err = h.service.Tasks().QuestionTool(ctx, args)
	case strings.HasSuffix(r.URL.Path, "approval"):
		choice, _ := args["choice"].(string)
		switch choice {
		case "approve", "once":
			args["action"] = "approve"
		case "deny", "reject":
			args["action"] = "request_changes"
		default:
			http.Error(w, "choice 必须为 approve 或 deny", http.StatusBadRequest)
			return
		}
		delete(args, "choice")
		out, err = h.service.Tasks().GraphTool(ctx, args)
	default:
		args["action"] = "resume"
		out, err = h.service.Tasks().GraphTool(ctx, args)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	respond(w, out)
}
