//go:build !linux

package bootstrap

import (
	"context"
	"os/exec"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args []string, directory string, environment []string) (string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	command.Env = append([]string(nil), environment...)
	buffer := &limitedBuffer{remaining: maxDiagnosticBytes}
	command.Stdout = buffer
	command.Stderr = buffer
	err := command.Run()
	return buffer.String(), err
}
