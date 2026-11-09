package server

import "net/http"

func (h *handler) steerRun(w http.ResponseWriter, r *http.Request) {
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err = decodeAPI(w, r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t, err := h.service.Tasks().Redirect(r.Context(), identity(r), id, in.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	respond(w, h.view(t))
}
