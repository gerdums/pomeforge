package pomeforge

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

func relativePathParts(root, target string, allowRoot bool) ([]string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("file must remain below its selected root")
	}
	if rel == "." {
		if allowRoot {
			return []string{}, nil
		}
		return nil, errors.New("file must remain below its selected root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("invalid file path")
		}
	}
	return parts, nil
}

// openDirectoryAbsolute acquires every component from the filesystem root with
// O_NOFOLLOW. A rename can leave the caller on the directory it already opened,
// but replacing any not-yet-opened ancestor with a symlink cannot redirect it.
func openDirectoryAbsolute(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("directory path must be absolute")
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if clean == string(filepath.Separator) {
		parts = nil
	}
	for _, part := range parts {
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), clean), nil
}

// openDirectoryWithin starts from an already safely acquired canonical root
// descriptor and opens target one component at a time without following links.
func openDirectoryWithin(root, target string) (*os.File, error) {
	parts, err := relativePathParts(root, target, true)
	if err != nil {
		return nil, err
	}
	rootDirectory, err := openDirectoryAbsolute(root)
	if err != nil {
		return nil, err
	}
	defer rootDirectory.Close()
	fd, err := syscall.Openat(int(rootDirectory.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), target), nil
}

// openRegularWithin acquires the selected root component-by-component, then
// walks to a nonblocking regular file using no-follow directory descriptors.
func openRegularWithin(root, target string, flags int, perm uint32) (*os.File, error) {
	parts, err := relativePathParts(root, target, false)
	if err != nil {
		return nil, err
	}
	directory, err := openDirectoryAbsolute(root)
	if err != nil {
		return nil, err
	}
	fd := int(directory.Fd())
	for _, part := range parts[:len(parts)-1] {
		nextName := filepath.Join(directory.Name(), part)
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		_ = directory.Close()
		if openErr != nil {
			return nil, openErr
		}
		directory = os.NewFile(uintptr(next), nextName)
		fd = next
	}
	fileFD, err := syscall.Openat(fd, parts[len(parts)-1], flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	_ = directory.Close()
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

func walkRegularFilesInProject(ctx context.Context, workspace, project string, skipRootDirectories map[string]bool, visit func(string, *os.File) error) error {
	projectDirectory, err := openDirectoryWithin(workspace, project)
	if err != nil {
		return err
	}
	return walkRegularDirectory(ctx, projectDirectory, "", skipRootDirectories, visit)
}

// walkRegularFilesWithin enumerates an already selected root through open
// directory descriptors. Each child is opened relative to its parent with
// O_NOFOLLOW, so renaming a visited directory and replacing its pathname with
// a symlink cannot redirect traversal outside root.
func walkRegularFilesWithin(ctx context.Context, root string, skipRootDirectories map[string]bool, visit func(string, *os.File) error) error {
	rootDirectory, err := openDirectoryAbsolute(root)
	if err != nil {
		return err
	}
	return walkRegularDirectory(ctx, rootDirectory, "", skipRootDirectories, visit)
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
