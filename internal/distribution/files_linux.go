//go:build linux

package distribution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func pathParts(name string) ([]string, error) {
	if err := requireAbsoluteCleanPath("path", name); err != nil {
		return nil, err
	}
	clean := filepath.Clean(name)
	if clean == string(filepath.Separator) {
		return nil, nil
	}
	return strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)), nil
}

func openAtFile(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openDirectoryNoFollow(name string) (*os.File, error) {
	parts, err := pathParts(name)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	for _, part := range parts {
		next, openErr := openAtFile(current, part, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		_ = current.Close()
		if openErr != nil {
			return nil, fmt.Errorf("open directory component %q without following links: %w", part, openErr)
		}
		current = next
	}
	info, err := current.Stat()
	if err != nil || !info.IsDir() {
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%q is not a directory", name)
	}
	return current, nil
}

func openRegularNoFollow(name string) (*os.File, os.FileInfo, error) {
	parts, err := pathParts(name)
	if err != nil {
		return nil, nil, err
	}
	if len(parts) == 0 {
		return nil, nil, fmt.Errorf("%q is not a regular file", name)
	}
	parent := filepath.Dir(name)
	dir, err := openDirectoryNoFollow(parent)
	if err != nil {
		return nil, nil, err
	}
	defer dir.Close()
	f, err := openAtFile(dir, filepath.Base(name), syscall.O_RDONLY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open %q as a regular file without following links (symlinks rejected): %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%q is not a regular file", name)
	}
	return f, info, nil
}

func createRegularNoFollow(name string, mode os.FileMode) (*os.File, func(bool), error) {
	if err := requireAbsoluteCleanPath("path", name); err != nil {
		return nil, nil, err
	}
	parent, err := openDirectoryNoFollow(filepath.Dir(name))
	if err != nil {
		return nil, nil, err
	}
	f, err := openAtFile(parent, filepath.Base(name), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, uint32(mode.Perm()))
	if err != nil {
		_ = parent.Close()
		return nil, nil, err
	}
	cleanup := func(keep bool) {
		if !keep {
			_ = syscall.Unlinkat(int(parent.Fd()), filepath.Base(name))
		}
		_ = parent.Close()
	}
	return f, cleanup, nil
}

func walkRegularTree(ctx context.Context, root string, maxEntries int, visit treeVisitor) error {
	return walkRegularTreeSkipping(ctx, root, maxEntries, nil, visit)
}

func walkRegularTreeSkipping(ctx context.Context, root string, maxEntries int, skip func(string) bool, visit treeVisitor) error {
	rootDir, err := openDirectoryNoFollow(root)
	if err != nil {
		return err
	}
	defer rootDir.Close()
	entries := 0
	var walk func(*os.File, string) error
	walk = func(dir *os.File, relative string) error {
		children, err := dir.ReadDir(-1)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := checkContext(ctx); err != nil {
				return err
			}
			entries++
			if entries > maxEntries {
				return fmt.Errorf("tree exceeds %d-entry limit", maxEntries)
			}
			name := child.Name()
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
				return fmt.Errorf("unsafe directory entry name %q", name)
			}
			rel := name
			if relative != "" {
				rel = filepath.Join(relative, name)
			}
			if skip != nil && skip(rel) {
				continue
			}
			opened, openErr := openAtFile(dir, name, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
			if openErr == nil {
				info, statErr := opened.Stat()
				if statErr != nil || !info.IsDir() {
					_ = opened.Close()
					if statErr != nil {
						return statErr
					}
					return fmt.Errorf("tree entry %q changed during directory open", rel)
				}
				skip, visitErr := visit(rel, info, nil)
				if visitErr == nil && !skip {
					visitErr = walk(opened, rel)
				}
				closeErr := opened.Close()
				if visitErr != nil {
					return visitErr
				}
				if closeErr != nil {
					return closeErr
				}
				continue
			}
			opened, fileErr := openAtFile(dir, name, syscall.O_RDONLY, 0)
			if fileErr != nil {
				return fmt.Errorf("tree contains unsupported nonregular or symlink entry %q: %w", rel, errors.Join(openErr, fileErr))
			}
			info, statErr := opened.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				_ = opened.Close()
				if statErr != nil {
					return statErr
				}
				return fmt.Errorf("tree contains unsupported nonregular entry %q", rel)
			}
			_, visitErr := visit(rel, info, opened)
			closeErr := opened.Close()
			if visitErr != nil {
				return visitErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}
	return walk(rootDir, "")
}
