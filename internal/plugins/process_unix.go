//go:build !windows

package plugins

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func processSupervisor(cmd *exec.Cmd) (func() error, func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = kill
	return nil, func() { _ = kill() }
}
