//go:build !unix

package builtin

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
