package winx

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PrepareChild sets how a program an extension starts appears: a console program gets no console window
// of its own; hide also starts a windowed program with its first window hidden.
func PrepareChild(cmd *exec.Cmd, hide bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: hide, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// ChildGroup holds the programs one extension started, with whatever those started in turn (a job object).
// Kill ends them all at once; the system does the same when this process is gone, however that happens.
type ChildGroup struct{ job windows.Handle }

func NewChildGroup() *ChildGroup {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &ChildGroup{}
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return &ChildGroup{}
	}
	return &ChildGroup{job: job}
}

func (g *ChildGroup) Add(pid int) {
	if g.job == 0 {
		return
	}
	if process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.AssignProcessToJobObject(g.job, process)
		windows.CloseHandle(process)
	}
}

// Kill ends every program of the group. The group is not used again afterwards.
func (g *ChildGroup) Kill() {
	if g.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(g.job, 1)
	windows.CloseHandle(g.job)
	g.job = 0
}
