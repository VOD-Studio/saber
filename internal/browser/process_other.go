//go:build !darwin && !linux && !windows

package browser

import (
	"errors"
	"os"
	"os/exec"
)

func processIdentity(int) (string, error) {
	return "", errors.New("owned host browser launch supports macOS, Linux and Windows; use a configured CDP endpoint on this platform")
}
func configureProcess(*exec.Cmd) error {
	return errors.New("host browser process ownership is unsupported on this platform; use a trusted CDP endpoint")
}
func killOwnedProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
func isGoneProcess(err error) bool { return errors.Is(err, os.ErrNotExist) }
