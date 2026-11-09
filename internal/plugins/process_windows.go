package plugins

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pluginJobs sync.Map

func processSupervisor(cmd *exec.Cmd) (func() error, func()) {
	cmd.Cancel = func() error {
		if handle, ok := pluginJobs.LoadAndDelete(cmd); ok {
			return windows.CloseHandle(handle.(windows.Handle))
		}
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return func() error { return attachProcessJob(cmd) }, func() { releaseProcessJob(cmd) }
}
func attachProcessJob(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	pluginJobs.Store(cmd, job)
	return nil
}
func releaseProcessJob(cmd *exec.Cmd) {
	if handle, ok := pluginJobs.LoadAndDelete(cmd); ok {
		_ = windows.CloseHandle(handle.(windows.Handle))
	}
}
