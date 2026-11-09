//go:build !windows

package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func lockCredential(ctx context.Context, path string) (func(), error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential lock directory must be private (0700)")
	}
	fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = unix.Close(fd)
			return nil, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
