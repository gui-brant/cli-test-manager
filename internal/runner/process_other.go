//go:build !unix

package runner

import (
	"os"
	"os/exec"
)

// On systems without Unix process groups only the command itself is stopped,
// not what it started. This path compiles but is not tested.

func setProcessGroup(*exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killGroup(cmd *exec.Cmd) { terminateGroup(cmd) }

func groupAlive(*exec.Cmd) bool { return false }

func signalExitCode(*os.ProcessState) int { return -1 }
