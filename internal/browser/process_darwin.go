//go:build darwin

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) && errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	if info == nil || info.Proc.P_pid != int32(pid) {
		return "", os.ErrNotExist
	}
	return fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), nil
}
func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}
func killOwnedProcess(pid int) error {
	err := unix.Kill(-pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
func isGoneProcess(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH)
}
