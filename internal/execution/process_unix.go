//go:build unix

package execution

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让每条命令拥有独立进程组，便于整组清理派生进程。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroup(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return 0
	}
	return pgid
}

func terminateGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGTERM) }

func killGroup(pgid int) error { return syscall.Kill(-pgid, syscall.SIGKILL) }

func groupExists(pgid int) bool { return pgid > 0 && syscall.Kill(-pgid, 0) == nil }

func signalGroup(pgid, signal int) error { return syscall.Kill(-pgid, syscall.Signal(signal)) }
