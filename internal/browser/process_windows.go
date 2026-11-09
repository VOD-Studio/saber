//go:build windows

package browser

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func processIdentity(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var created, exit, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(handle, &created, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", created.HighDateTime, created.LowDateTime), nil
}
func configureProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	return nil
}
func killOwnedProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}
func isGoneProcess(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND)
}
