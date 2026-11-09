package server

import "time"

func (h *handler) pollInterval() time.Duration {
	if h.service != nil && h.service.Config() != nil && h.service.Config().API.PollIntervalMS > 0 {
		return time.Duration(h.service.Config().API.PollIntervalMS) * time.Millisecond
	}
	return 100 * time.Millisecond
}
