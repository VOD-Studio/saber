//go:build linux

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	at := strings.LastIndex(string(data), ") ")
	if at < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data)[at+2:])
	if len(fields) <= 19 {
		return "", errors.New("incomplete process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(ticks, 10), nil
}
func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
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
