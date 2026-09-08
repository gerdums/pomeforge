//go:build linux

package pomeforge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"unsafe"
)

const atRemoveDir = 0x200

// installGeneratedIconFiles performs every catalog mutation relative to an
// already-open no-follow directory descriptor. A concurrent pathname or
// symlink replacement therefore cannot redirect writes or cleanup outside the
// selected project.
func installGeneratedIconFiles(project string, files map[string][]byte) error {
	assetsPath := filepath.Join(project, "Assets.xcassets")
	assets, err := openDirectoryWithin(project, assetsPath)
	if err != nil {
		return errors.New("asset catalog is missing, unsafe, or not a directory")
	}
	defer assets.Close()

	current, err := openIconDirectoryAt(assets, "AppIcon.appiconset")
	if err != nil {
		return errors.New("AppIcon.appiconset is missing, unsafe, or not a directory")
	}
	marker, err := openRegularAt(current, placeholderMarker, syscall.O_RDONLY, 0)
	if err != nil {
		current.Close()
		return errors.New("starter-art ownership marker is absent or unsafe; refusing to overwrite a customized icon set")
	}
	marker.Close()

	if existsAt(assets, ".pomeforge-placeholder-backup") {
		current.Close()
		return errors.New("stale icon replacement backup exists; inspect it before retrying")
	}
	temporaryName, temporary, err := createTemporaryDirectoryAt(assets)
	if err != nil {
		current.Close()
		return err
	}
	temporaryInstalled := false
	defer func() {
		if temporaryInstalled {
			return
		}
		_ = removeOpenDirectoryContents(temporary)
		_ = temporary.Close()
		_ = unlinkDirectoryAt(int(assets.Fd()), temporaryName)
	}()
	if err := writeIconFilesAt(temporary, files); err != nil {
		current.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		current.Close()
		return err
	}
	if err := syscall.Renameat(int(assets.Fd()), "AppIcon.appiconset", int(assets.Fd()), ".pomeforge-placeholder-backup"); err != nil {
		current.Close()
		return err
	}
	if err := syscall.Renameat(int(assets.Fd()), temporaryName, int(assets.Fd()), "AppIcon.appiconset"); err != nil {
		_ = syscall.Renameat(int(assets.Fd()), ".pomeforge-placeholder-backup", int(assets.Fd()), "AppIcon.appiconset")
		current.Close()
		return err
	}
	temporaryInstalled = true
	_ = temporary.Close()
	if err := assets.Sync(); err != nil {
		current.Close()
		return fmt.Errorf("custom icons installed but catalog directory sync failed: %w", err)
	}
	if err := removeOpenDirectoryContents(current); err != nil {
		current.Close()
		return fmt.Errorf("custom icons installed but placeholder backup cleanup failed: %w", err)
	}
	if err := current.Close(); err != nil {
		return fmt.Errorf("custom icons installed but placeholder backup close failed: %w", err)
	}
	if err := unlinkDirectoryAt(int(assets.Fd()), ".pomeforge-placeholder-backup"); err != nil {
		return fmt.Errorf("custom icons installed but placeholder backup cleanup failed: %w", err)
	}
	return assets.Sync()
}

func openIconDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openRegularAt(parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, errors.New("unsafe generated filename")
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return nil, errors.New("generated path is not a regular file")
	}
	return os.NewFile(uintptr(fd), name), nil
}

func existsAt(parent *os.File, name string) bool {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = syscall.Close(fd)
		return true
	}
	return !errors.Is(err, syscall.ENOENT)
}

func createTemporaryDirectoryAt(parent *os.File) (string, *os.File, error) {
	for attempt := 0; attempt < 32; attempt++ {
		bytes := make([]byte, 12)
		if _, err := rand.Read(bytes); err != nil {
			return "", nil, err
		}
		name := ".pomeforge-icons-" + hex.EncodeToString(bytes)
		if err := syscall.Mkdirat(int(parent.Fd()), name, 0o755); errors.Is(err, syscall.EEXIST) {
			continue
		} else if err != nil {
			return "", nil, err
		}
		directory, err := openIconDirectoryAt(parent, name)
		if err != nil {
			_ = unlinkDirectoryAt(int(parent.Fd()), name)
			return "", nil, err
		}
		return name, directory, nil
	}
	return "", nil, errors.New("could not reserve a fresh icon staging directory")
}

func writeIconFilesAt(directory *os.File, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := openRegularAt(directory, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(files[name]); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func removeOpenDirectoryContents(directory *os.File) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child, openErr := openIconDirectoryAt(directory, entry.Name())
		if openErr == nil {
			if err := removeOpenDirectoryContents(child); err != nil {
				child.Close()
				return err
			}
			if err := child.Close(); err != nil {
				return err
			}
			if err := unlinkDirectoryAt(int(directory.Fd()), entry.Name()); err != nil {
				return err
			}
			continue
		}
		if err := syscall.Unlinkat(int(directory.Fd()), entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func unlinkDirectoryAt(directoryFD int, name string) error {
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_UNLINKAT, uintptr(directoryFD), uintptr(unsafe.Pointer(pointer)), atRemoveDir, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
