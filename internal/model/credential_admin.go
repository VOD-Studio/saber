package model

import (
	"context"
	"errors"
	"os"
	"time"

	"rua.plus/saber/internal/config"
)

var errCredentialsUnavailable = errors.New("no healthy provider credentials available")

func (a *authManager) poolCommand(ctx context.Context, provider string, cfg config.ProviderConfig, args []string) (any, error) {
	pool := newCredentialPool(provider, cfg, a)
	unlock, err := lockCredential(ctx, pool.path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	states := map[string]credentialState{}
	if err = readPrivateJSON(pool.path, &states); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	switch args[0] {
	case "pool-status":
		result := []map[string]any{}
		for _, entry := range pool.entries() {
			state := states[entry.ID]
			result = append(result, map[string]any{"id": entry.ID, "disabled": entry.Disabled, "revoked": state.Dead, "cooldown_until": state.Until, "model_cooldowns": state.ModelCooldowns, "requests": state.Uses, "oauth": entry.TokenFile != "", "available_after_cooldown": !entry.Disabled && !state.Dead && !time.Now().Before(state.Until)})
		}
		return map[string]any{"provider": provider, "strategy": cfg.PoolStrategy, "credentials": result}, nil
	case "pool-reset":
		id := ""
		if len(args) > 2 {
			id = args[2]
		}
		found := false
		for _, entry := range pool.entries() {
			if id == "" || entry.ID == id {
				state := states[entry.ID]
				if state.Dead {
					return nil, errors.New("revoked credentials require a new credential or OAuth login; cooldown reset cannot revive them")
				}
				state.Until = time.Time{}
				state.ModelCooldowns = nil
				states[entry.ID] = state
				found = true
			}
		}
		if !found {
			return nil, errors.New("credential ID is not configured")
		}
		if err = writePrivateJSON(pool.path, states); err != nil {
			return nil, err
		}
		return map[string]any{"provider": provider, "cooldown_reset": true}, nil
	default:
		return nil, errors.New("unknown credential pool operation")
	}
}
