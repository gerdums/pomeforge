//go:build linux

package bootstrap

import (
	"context"
	"errors"
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
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	buffer := &limitedBuffer{remaining: maxDiagnosticBytes}
	command.Stdout = buffer
	command.Stderr = buffer
	err := command.Run()
	return buffer.String(), err
}
