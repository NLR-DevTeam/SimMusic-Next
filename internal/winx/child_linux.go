package winx

import (
	"os/exec"
	"syscall"
)

// PrepareChild 让扩展启动的程序自成一个进程组，并在本进程退出时一并结束；Linux 上没有需要隐藏的控制台窗口。
func PrepareChild(cmd *exec.Cmd, hide bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}

// ChildGroup 是一个扩展启动的全部程序，连同它们再启动的进程（各自的进程组）。
type ChildGroup struct{ pids []int }

func NewChildGroup() *ChildGroup { return &ChildGroup{} }

func (g *ChildGroup) Add(pid int) { g.pids = append(g.pids, pid) }

// Kill 结束组内的全部进程，此后不再使用这个组。
func (g *ChildGroup) Kill() {
	for _, pid := range g.pids {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	g.pids = nil
}
