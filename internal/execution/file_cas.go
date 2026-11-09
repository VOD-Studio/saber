package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

func checkExpectedDigest(args map[string]any, content []byte, exists bool) error {
	value, ok := args["expected_sha256"]
	if !ok {
		return nil
	}
	if !exists {
		return errors.New("expected_sha256 requires an existing file")
	}
	sum := sha256.Sum256(content)
	if value != hex.EncodeToString(sum[:]) {
		return errors.New("file SHA-256 changed; reload current content")
	}
	return nil
}

func (e *Executor) currentRemote(run scope) *remoteEnvironment {
	e.envMu.Lock()
	entry := e.envs[run.id]
	e.envMu.Unlock()
	e.derivedMu.Lock()
	if derived := e.derived[run.dir]; derived != nil {
		entry = derived.entry
	}
	e.derivedMu.Unlock()
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	remote, _ := entry.environment.(*remoteEnvironment)
	return remote
}

func (e *Executor) authorizeProgram(ctx context.Context, env map[string]string) error {
	return ValidateEnvironment(env, e.forbiddenSecrets)
}
