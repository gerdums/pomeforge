//go:build !linux

package pomeforge

import (
	"context"
	"errors"
	"os"
)

func copyOwnedTree(context.Context, string, string) error {
	return errors.New("safe SDK tree staging is supported only on Linux")
}

func copyOwnedTreeToParent(context.Context, *os.File, string, *os.File, string, string) error {
	return errors.New("safe SDK tree staging is supported only on Linux")
}

func createFreshPrivateDirectoryWithin(string, string) (*os.File, error) {
	return nil, errors.New("safe SDK staging directory creation is supported only on Linux")
}

func verifyFreshPrivateDirectoryWithin(string, string, *os.File) error {
	return errors.New("safe SDK staging directory verification is supported only on Linux")
}

func replaceDirectoryWithOwnedTree(context.Context, string, string, string) error {
	return errors.New("safe SDK header replacement is supported only on Linux")
}

func replaceDirectoryThroughContainedAlias(context.Context, string, string, string, string, string) error {
	return errors.New("safe SDK header alias resolution is supported only on Linux")
}

func writeExclusiveFileWithin(string, string, []byte, uint32) error {
	return errors.New("safe SDK metadata snapshots are supported only on Linux")
}

func findNamedRegularFilesWithin(context.Context, string, string, int) ([]string, error) {
	return nil, errors.New("safe installed SDK inspection is supported only on Linux")
}
