package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"rua.plus/saber/internal/config"
)

func (s *Service) speechProvider(ctx context.Context, p config.MediaProviderConfig, tag string) (config.MediaProviderConfig, error) {
	if p.Provider != "deepinfra" || p.Model != "" {
		return p, nil
	}
	base, key, err := providerRuntime(p)
	if err != nil {
		return p, err
	}
	fingerprint := sha256.Sum256([]byte(base + "\x00" + key + "\x00" + tag))
	cacheKey := hex.EncodeToString(fingerprint[:])
	s.mu.Lock()
	cached := s.speechModels[cacheKey]
	s.mu.Unlock()
	if cached != "" {
		p.Model = cached
		return p, nil
	}
	r, err := s.jsonRequest(ctx, p, "GET", base+"/models?filter=true&sort_by=hermes", nil)
	if err != nil {
		return p, fmt.Errorf("DeepInfra %s catalog unavailable; pin model in media_tools: %w", tag, err)
	}
	for _, v := range list(r, "data") {
		item, _ := v.(map[string]any)
		id := str(item, "id")
		metadata := object(item, "metadata")
		if id == "" || metadata == nil {
			continue
		}
		for _, t := range list(metadata, "tags") {
			if t == tag {
				p.Model = id
				s.mu.Lock()
				if s.speechModels == nil {
					s.speechModels = make(map[string]string)
				}
				s.speechModels[cacheKey] = id
				s.mu.Unlock()
				return p, nil
			}
		}
	}
	return p, fmt.Errorf("DeepInfra has no served %s model; pin media_tools provider model", tag)
}
