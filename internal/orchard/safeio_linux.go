package orchard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// openRegularWithin walks from an already selected root using no-follow
// directory descriptors, then opens a nonblocking regular file. This keeps a
// concurrent symlink or special-file replacement from escaping or blocking the
// caller between an earlier path check and open.
func openRegularWithin(root, target string, flags int, perm uint32) (*os.File, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("file must remain below its selected root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid file path")
		}
	}

	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	fileFD, err := syscall.Openat(fd, parts[len(parts)-1], flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	_ = syscall.Close(fd)
	if err != nil {
		return nil, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fileFD, &stat); err != nil {
		_ = syscall.Close(fileFD)
		return nil, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fileFD)
		return nil, fmt.Errorf("file is not regular")
	}
	return os.NewFile(uintptr(fileFD), target), nil
}

func openRegularAbsolute(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("executable path is not absolute")
	}
	return openRegularWithin(string(filepath.Separator), clean, syscall.O_RDONLY, 0)
}

// walkRegularFilesWithin enumerates an already selected root through open
// directory descriptors. Each child is opened relative to its parent with
// O_NOFOLLOW, so renaming a visited directory and replacing its pathname with
// a symlink cannot redirect traversal outside root.
func walkRegularFilesWithin(ctx context.Context, root string, skipRootDirectories map[string]bool, visit func(string, *os.File) error) error {
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	return walkRegularDirectory(ctx, os.NewFile(uintptr(rootFD), root), "", skipRootDirectories, visit)
}

func walkRegularDirectory(ctx context.Context, directory *os.File, prefix string, skipRootDirectories map[string]bool, visit func(string, *os.File) error) error {
	defer directory.Close()
	entries := make([]os.DirEntry, 0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, readErr := directory.ReadDir(256)
		entries = append(entries, batch...)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		relative := name
		if prefix != "" {
			relative = filepath.Join(prefix, name)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return Errorf("symlink_not_allowed", "project contains a symlink: "+relative)
		}
		if entry.Type() != 0 && !entry.IsDir() {
			return Errorf("invalid_project", "project contains a nonregular file: "+relative)
		}
		childFD, openErr := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if errors.Is(openErr, syscall.ELOOP) {
			return Errorf("symlink_not_allowed", "project contains a symlink: "+relative)
		}
		if openErr != nil {
			return openErr
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(childFD, &stat); err != nil {
			_ = syscall.Close(childFD)
			return err
		}
		switch stat.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			if prefix == "" && skipRootDirectories[name] {
				_ = syscall.Close(childFD)
				continue
			}
			if err := walkRegularDirectory(ctx, os.NewFile(uintptr(childFD), filepath.Join(directory.Name(), name)), relative, skipRootDirectories, visit); err != nil {
				return err
			}
		case syscall.S_IFREG:
			file := os.NewFile(uintptr(childFD), relative)
			visitErr := visit(relative, file)
			closeErr := file.Close()
			if visitErr != nil {
				return visitErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			_ = syscall.Close(childFD)
			return Errorf("invalid_project", "project contains a nonregular file: "+relative)
		}
	}
	return nil
}
