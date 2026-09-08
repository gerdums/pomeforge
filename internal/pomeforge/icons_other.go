//go:build !linux

package pomeforge

import "errors"

func installGeneratedIconFiles(string, map[string][]byte) error {
	return errors.New("secure icon catalog replacement is supported only on Linux")
}
