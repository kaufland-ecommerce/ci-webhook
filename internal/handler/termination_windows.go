//go:build windows

package handler

import (
	"os/exec"
	"strconv"
	"syscall"
)

// sendKillSignal sends terminate/kill signal to the process
func sendKillSignal(executor *Executor, pid int, signal syscall.Signal) error {

	err := exec.Command("TASKKILL", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()

	if err != nil {
		executor.logger.Error("error during handling terminate/kill signal", "error", err)
	}

	return err
}

// setPGID mock for windows build
func setPGID(cmd *exec.Cmd) {}
