package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type processRecord struct {
	PID        int    `json:"pid"`
	Start      string `json:"start"`
	Owner      int    `json:"owner"`
	OwnerStart string `json:"owner_start"`
	Profile    string `json:"profile"`
	Ephemeral  bool   `json:"ephemeral"`
}

func recordProcess(root string, pid int, profile string, ephemeral bool) (string, error) {
	start, err := processIdentity(pid)
	if err != nil {
		return "", err
	}
	ownerStart, err := processIdentity(os.Getpid())
	if err != nil {
		return "", err
	}
	record := processRecord{PID: pid, Start: start, Owner: os.Getpid(), OwnerStart: ownerStart, Profile: profile, Ephemeral: ephemeral}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, fmt.Sprintf("process-%d-%s.json", pid, digest(start)[:12]))
	if err = os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func reapOrphans(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "process-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("browser process ownership record has unsafe permissions: %s", entry.Name())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record processRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.PID <= 0 || record.Owner <= 0 || record.Start == "" || record.OwnerStart == "" {
			return errors.New("invalid browser process ownership record")
		}
		ownerStart, ownerErr := processIdentity(record.Owner)
		if ownerErr == nil && ownerStart == record.OwnerStart {
			continue
		}
		if ownerErr != nil && !isGoneProcess(ownerErr) {
			return fmt.Errorf("cannot safely determine browser owner liveness: %w", ownerErr)
		}
		start, childErr := processIdentity(record.PID)
		if childErr == nil && start == record.Start {
			if err = killOwnedProcess(record.PID); err != nil {
				return err
			}
		} else if childErr != nil && !isGoneProcess(childErr) {
			return fmt.Errorf("cannot safely identify orphaned browser: %w", childErr)
		}
		if record.Ephemeral {
			rel, err := filepath.Rel(root, record.Profile)
			if err != nil || rel == "." || strings.ContainsAny(rel, "/\\") || !strings.HasPrefix(rel, "session-") {
				return errors.New("orphan profile lies outside private browser session store")
			}
			if err = os.RemoveAll(record.Profile); err != nil {
				return err
			}
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
