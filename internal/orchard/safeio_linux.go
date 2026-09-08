package orchard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
