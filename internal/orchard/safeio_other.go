//go:build !linux

package orchard

import (
	"context"
	"errors"
	"os"
)

func openRegularWithin(string, string, int, uint32) (*os.File, error) {
	return nil, errors.New("secure project file access is supported only on Linux")
}

func openRegularAbsolute(string) (*os.File, error) {
	return nil, errors.New("secure executable inspection is supported only on Linux")
}

func walkRegularFilesWithin(context.Context, string, map[string]bool, func(string, *os.File) error) error {
	return errors.New("secure project traversal is supported only on Linux")
}
