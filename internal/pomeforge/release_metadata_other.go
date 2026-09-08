//go:build !linux

package pomeforge

import "errors"

func rewriteBundleInfoFile(string, string, func([]byte) ([]byte, error)) error {
	return errors.New("secure release metadata updates are supported only on Linux")
}
