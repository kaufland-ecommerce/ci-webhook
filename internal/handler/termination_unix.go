//go:build darwin || linux

package handler

import (
	"os/exec"
	"syscall"
)

// sendKillSignal sends terminate/kill signal to the process
func sendKillSignal(executor *Executor, pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err != nil {
		executor.logger.Error("Error during handling terminate/kill signal", "signal", signal.String(), "error", err)
		return err
	}
	return nil
}

// setPGID sets the same PGID for the child processes
func setPGID(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
