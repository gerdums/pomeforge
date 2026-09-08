//go:build !linux

package distribution

import (
	"context"
	"errors"
	"os"
)

var errLinuxSafeFiles = errors.New("safe distribution file access is supported only on Linux")

func openDirectoryNoFollow(string) (*os.File, error)            { return nil, errLinuxSafeFiles }
func openRegularNoFollow(string) (*os.File, os.FileInfo, error) { return nil, nil, errLinuxSafeFiles }
func createRegularNoFollow(string, os.FileMode) (*os.File, func(bool), error) {
	return nil, nil, errLinuxSafeFiles
}
func walkRegularTree(context.Context, string, int, treeVisitor) error { return errLinuxSafeFiles }
func walkRegularTreeSkipping(context.Context, string, int, func(string) bool, treeVisitor) error {
	return errLinuxSafeFiles
}
