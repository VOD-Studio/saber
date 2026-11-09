package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (h *handler) bindRunPaths(r *http.Request) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.PathValue("run"), "run_"), 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("无效任务编号")
	}
	if s := r.URL.Query().Get("session"); s != "" {
		if !identifier.MatchString(s) {
			return fmt.Errorf("无效会话编号")
		}
		r.SetPathValue("session", s)
		if _, err = h.service.Tasks().Get(r.Context(), session(r), id); err != nil {
			return fmt.Errorf("任务不存在于当前会话")
		}
	} else {
		r.SetPathValue("session", "api-principal")
		t, err := h.service.Tasks().ResolveOwnedTask(r.Context(), identity(r), id)
		if err != nil {
			return fmt.Errorf("任务不存在或当前身份无权访问")
		}
		r.SetPathValue("session", t.Message.Session.Conversation)
	}
	r.SetPathValue("task", strconv.FormatInt(id, 10))
	return nil
}
