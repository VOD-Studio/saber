package browser

import (
	"context"
	"strings"
)

func localRoute(key string) bool {
	return strings.HasSuffix(key, "::local") || strings.HasSuffix(key, "::profile")
}
func (s *Service) selectedSession(key string) *browserSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.sessions[s.active[key]]; b != nil {
		return b
	}
	if b := s.sessions[key]; b != nil {
		return b
	}
	if b := s.sessions[key+"::local"]; b != nil {
		return b
	}
	return s.sessions[key+"::profile"]
}

func (b *browserSession) route(ctx context.Context, raw string) (*browserSession, error) {
	cfg := b.owner.cfg
	if !cfg.AutoLocalForPrivateURLs || cfg.Provider == "local" || cfg.CDPURL != "" || raw == "about:blank" {
		return b, nil
	}
	private, err := urlPrivate(ctx, raw)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(strings.TrimSuffix(b.key, "::local"), "::profile")
	key := base
	if private {
		key += "::local"
	}
	if key == b.key {
		return b, nil
	}
	if private {
		if err = b.checkURL(ctx, raw); err != nil {
			return nil, err
		}
	}
	next, err := b.owner.get(ctx, key, b.label, private)
	if err != nil {
		return nil, err
	}
	b.owner.mu.Lock()
	b.owner.active[base] = key
	b.owner.mu.Unlock()
	return next, nil
}
