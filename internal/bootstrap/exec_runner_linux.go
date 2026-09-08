//go:build linux

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args []string, directory string, environment []string) (string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if command.Process == nil || command.Process.Pid <= 0 {
			return os.ErrProcessDone
		}
		return killProcessGroup(command.Process.Pid)
	}
	buffer := &limitedBuffer{remaining: maxDiagnosticBytes}
	command.Stdout = buffer
	command.Stderr = buffer
	if err := command.Start(); err != nil {
		return buffer.String(), err
	}

	// Setpgid makes the started process the leader of a new process group, so
	// its positive PID is also the only group ID this runner may signal. Wait
	// can return while descendants remain alive, including after WaitDelay has
	// closed inherited output pipes, so always terminate that group afterward.
	processGroupID := command.Process.Pid
	runErr := command.Wait()
	if cleanupErr := killProcessGroup(processGroupID); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
		runErr = errors.Join(runErr, fmt.Errorf("terminate process group %d: %w", processGroupID, cleanupErr))
	}
	return buffer.String(), runErr
}

func killProcessGroup(processGroupID int) error {
	if processGroupID <= 0 {
		return fmt.Errorf("invalid process group ID %d", processGroupID)
	}
	if err := syscall.Kill(-processGroupID, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	} else {
		return err
	}
}
