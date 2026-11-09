//go:build windows

package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func lockCredential(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path + ".lock"); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("unsafe credential lock")
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped); _ = f.Close() }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
