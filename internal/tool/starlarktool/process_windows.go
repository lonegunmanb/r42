//go:build windows

package starlarktool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func configureWorkerProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func terminateWorkerProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	command := exec.Command("taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("terminating starlark worker process tree: %w: %s", err, output)
	}
	return nil
}
