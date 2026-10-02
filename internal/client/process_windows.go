//go:build windows

package client

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no process groups, so a running agent is assigned to a job
// object as soon as it starts. Children created afterwards inherit the job,
// which lets killProcess terminate a command shim such as npx.cmd together
// with the real agent process it launched.
var (
	jobsMu sync.Mutex
	jobs   = map[*exec.Cmd]windows.Handle{}
)

func configureProcess(cmd *exec.Cmd) {
	// exec.CommandContext otherwise kills only the direct child.
	cmd.Cancel = func() error {
		killProcess(cmd)
		return nil
	}
}

// postStart runs immediately after Cmd.Start, while the child is still
// starting, so every process it spawns later inherits the job.
func postStart(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	jobsMu.Lock()
	jobs[cmd] = job
	jobsMu.Unlock()
}

func killProcess(cmd *exec.Cmd) {
	jobsMu.Lock()
	job, ok := jobs[cmd]
	if ok {
		delete(jobs, cmd)
	}
	jobsMu.Unlock()
	if ok {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return
	}
	// Fall back to the direct child when the job could not be assigned, for
	// example under an outer job that does not allow nesting.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
