//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }

// Windows 没有 SIGSTOP/SIGCONT，也没有能直接发给进程组的 TERM：
// 挂起/恢复/TERM 一律报不支持，由 supervisor 提示“该执行器在 Windows 上不支持进程挂起”；
// 终止用 taskkill /T /F 杀整棵树。
func pauseGroup(int) error  { return ErrSignalUnsupported }
func resumeGroup(int) error { return ErrSignalUnsupported }
func termGroup(int) error   { return ErrSignalUnsupported }

func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	kill := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"),
		"/T", "/F", "/PID", strconv.Itoa(pid))
	const createNoWindow = 0x08000000
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return kill.Run()
}
