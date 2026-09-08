//go:build !linux

package orchard

import (
	"errors"
	"os"
)

func openRegularWithin(string, string, int, uint32) (*os.File, error) {
	return nil, errors.New("secure project file access is supported only on Linux")
}

func openRegularAbsolute(string) (*os.File, error) {
	return nil, errors.New("secure executable inspection is supported only on Linux")
}
