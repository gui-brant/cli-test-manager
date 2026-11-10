//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in a new process group of its own, so that
// the command and everything it starts can be stopped together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil {
		// A negative pid addresses the whole group. An error means the group
		// is already gone, which is the outcome wanted.
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

// terminateGroup asks the group to stop, so tools can clean up after themselves.
func terminateGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

// killGroup stops the group at once, whether it agrees or not.
func killGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }

// groupAlive reports whether any process of the command's group still exists.
func groupAlive(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signalExitCode gives the shell's convention for a command killed by a
// signal, 128 plus the signal number (143 for terminate, 137 for kill), or -1
// when the command did not die from a signal.
func signalExitCode(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return -1
}
