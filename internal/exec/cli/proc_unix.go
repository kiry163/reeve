//go:build !windows

package cli

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup 让 CLI 成为新进程组的组长：它派生的子进程也在这个组里，
// 暂停/恢复/终止才能覆盖整棵进程树（npm shim 会再 exec 一个 node）。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }

func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil // 组里已经没有进程了，视为成功
		}
		return err
	}
	return nil
}

func pauseGroup(pgid int) error  { return signalGroup(pgid, syscall.SIGSTOP) }
func resumeGroup(pgid int) error { return signalGroup(pgid, syscall.SIGCONT) }
func termGroup(pgid int) error   { return signalGroup(pgid, syscall.SIGTERM) }
func killGroup(pgid int) error   { return signalGroup(pgid, syscall.SIGKILL) }
