package orchard

import (
	"bytes"
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

type sdkCopyBudget struct {
	entries int
	bytes   int64
}

func descriptorPath(fd int, name string) string {
	if name == "" {
		return fmt.Sprintf("/proc/self/fd/%d", fd)
	}
	return fmt.Sprintf("/proc/self/fd/%d/%s", fd, name)
}

func copyOwnedTree(ctx context.Context, source, destination string) error {
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || source == string(filepath.Separator) || destination == string(filepath.Separator) {
		return errors.New("SDK tree paths must be absolute non-root paths")
	}
	sourceDirectory, err := openDirectoryAbsolute(source)
	if err != nil {
		return err
	}
	defer sourceDirectory.Close()
	parent, err := openDirectoryAbsolute(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer parent.Close()
	return copyOwnedTreeToParent(ctx, sourceDirectory, source, parent, filepath.Base(destination), destination)
}

func copyOwnedTreeToParent(ctx context.Context, sourceDirectory *os.File, sourceRoot string, destinationParent *os.File, destinationName, destinationRoot string) error {
	if destinationName == "" || destinationName == "." || destinationName == ".." || strings.ContainsRune(destinationName, filepath.Separator) {
		return errors.New("invalid SDK tree destination name")
	}
	sourceInfo, err := sourceDirectory.Stat()
	if err != nil || !sourceInfo.IsDir() {
		return errors.New("SDK tree source must be a directory")
	}
	if err := syscall.Mkdirat(int(destinationParent.Fd()), destinationName, uint32(sourceInfo.Mode().Perm()|0o700)); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return errors.New("copy destination already exists")
		}
		return err
	}
	destinationFD, err := syscall.Openat(int(destinationParent.Fd()), destinationName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	destinationDirectory := os.NewFile(uintptr(destinationFD), destinationRoot)
	defer destinationDirectory.Close()
	budget := &sdkCopyBudget{}
	if err := copyOpenedSDKDirectory(ctx, sourceDirectory, destinationDirectory, sourceRoot, destinationRoot, "", budget); err != nil {
		return err
	}
	return syscall.Fchmod(destinationFD, uint32(sourceInfo.Mode().Perm()|0o700))
}

func copyOpenedSDKDirectory(ctx context.Context, source, destination *os.File, sourceRoot, destinationRoot, relative string, budget *sdkCopyBudget) error {
	entries, err := source.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
			return errors.New("source tree contains an invalid entry name")
		}
		budget.entries++
		if budget.entries > maxSDKTreeEntries {
			return errors.New("source tree contains too many entries")
		}
		childRelative := filepath.Join(relative, name)
		childFD, openErr := syscall.Openat(int(source.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if errors.Is(openErr, syscall.ELOOP) {
			link, err := os.Readlink(descriptorPath(int(source.Fd()), name))
			if err != nil {
				return err
			}
			rebased, err := safeRebasedSDKLink(sourceRoot, destinationRoot, childRelative, link, descriptorPath(int(source.Fd()), name))
			if err != nil {
				return err
			}
			if err := os.Symlink(rebased, descriptorPath(int(destination.Fd()), name)); err != nil {
				return err
			}
			continue
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
			if err := syscall.Mkdirat(int(destination.Fd()), name, uint32(stat.Mode&0o777|0o700)); err != nil {
				_ = syscall.Close(childFD)
				return err
			}
			destinationChildFD, err := syscall.Openat(int(destination.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			if err != nil {
				_ = syscall.Close(childFD)
				return err
			}
			sourceChild := os.NewFile(uintptr(childFD), filepath.Join(source.Name(), name))
			destinationChild := os.NewFile(uintptr(destinationChildFD), filepath.Join(destination.Name(), name))
			copyErr := copyOpenedSDKDirectory(ctx, sourceChild, destinationChild, sourceRoot, destinationRoot, childRelative, budget)
			if chmodErr := syscall.Fchmod(destinationChildFD, uint32(stat.Mode&0o777|0o700)); copyErr == nil {
				copyErr = chmodErr
			}
			if closeErr := sourceChild.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if closeErr := destinationChild.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return copyErr
			}
		case syscall.S_IFREG:
			if stat.Size < 0 || budget.bytes > maxSDKInputBytes-stat.Size {
				_ = syscall.Close(childFD)
				return errors.New("source tree exceeds its byte limit")
			}
			budget.bytes += stat.Size
			targetFD, err := syscall.Openat(int(destination.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, uint32(stat.Mode&0o777|0o200))
			if err != nil {
				_ = syscall.Close(childFD)
				return err
			}
			sourceFile := os.NewFile(uintptr(childFD), childRelative)
			targetFile := os.NewFile(uintptr(targetFD), childRelative)
			written, copyErr := copyContext(ctx, targetFile, sourceFile, stat.Size)
			if copyErr == nil && written != stat.Size {
				copyErr = errors.New("source tree file changed while copying")
			}
			if syncErr := targetFile.Sync(); copyErr == nil {
				copyErr = syncErr
			}
			if chmodErr := syscall.Fchmod(targetFD, uint32(stat.Mode&0o777)); copyErr == nil {
				copyErr = chmodErr
			}
			if closeErr := sourceFile.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if closeErr := targetFile.Close(); copyErr == nil {
				copyErr = closeErr
			}
			if copyErr != nil {
				return copyErr
			}
		default:
			_ = syscall.Close(childFD)
			return fmt.Errorf("source tree contains a special file: %s", childRelative)
		}
	}
	return nil
}

func safeRebasedSDKLink(sourceRoot, destinationRoot, relative, link, descriptor string) (string, error) {
	lexical := link
	if !filepath.IsAbs(lexical) {
		lexical = filepath.Join(sourceRoot, filepath.Dir(relative), lexical)
	}
	lexical = filepath.Clean(lexical)
	if !lexicalPathInsideAny(lexical, sourceRoot) {
		return "", fmt.Errorf("source tree symbolic link escapes its root: %s", relative)
	}
	resolved, err := filepath.EvalSymlinks(descriptor)
	if err != nil || !pathInsideAny(resolved, sourceRoot) {
		return "", fmt.Errorf("source tree symbolic link is broken or escapes its root: %s", relative)
	}
	if !filepath.IsAbs(link) {
		return link, nil
	}
	targetRelative, err := filepath.Rel(sourceRoot, lexical)
	if err != nil {
		return "", err
	}
	mapped := filepath.Join(destinationRoot, targetRelative)
	return filepath.Rel(filepath.Join(destinationRoot, filepath.Dir(relative)), mapped)
}

func openOrCreateDirectoryAt(parent *os.File, parts []string) (*os.File, error) {
	currentFD, err := syscall.Openat(int(parent.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(currentFD), parent.Name())
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, filepath.Separator) {
			current.Close()
			return nil, errors.New("invalid contained directory path")
		}
		nextFD, openErr := syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if errors.Is(openErr, syscall.ENOENT) {
			if err := syscall.Mkdirat(int(current.Fd()), part, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
				current.Close()
				return nil, err
			}
			nextFD, openErr = syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			current.Close()
			return nil, fmt.Errorf("contained directory %s is unavailable or a symlink: %w", part, openErr)
		}
		next := os.NewFile(uintptr(nextFD), filepath.Join(current.Name(), part))
		current.Close()
		current = next
	}
	return current, nil
}

func createFreshPrivateDirectoryWithin(root, target string) (*os.File, error) {
	parts, err := relativePathParts(root, target, false)
	if err != nil {
		return nil, err
	}
	parentPath := root
	if len(parts) > 1 {
		parentPath = filepath.Join(root, filepath.Join(parts[:len(parts)-1]...))
	}
	parent, err := openDirectoryWithin(root, parentPath)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := parts[len(parts)-1]
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return nil, errors.New("private directory destination already exists")
		}
		return nil, err
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open fresh private directory without following links: %w", err)
	}
	directory := os.NewFile(uintptr(fd), target)
	if err := syscall.Fchmod(fd, 0o700); err != nil {
		directory.Close()
		return nil, err
	}
	if err := verifyFreshPrivateDirectoryWithin(root, target, directory); err != nil {
		directory.Close()
		return nil, err
	}
	return directory, nil
}

func verifyFreshPrivateDirectoryWithin(root, target string, held *os.File) error {
	if held == nil {
		return errors.New("fresh private directory descriptor is missing")
	}
	heldInfo, err := held.Stat()
	if err != nil || !heldInfo.IsDir() || heldInfo.Mode().Perm() != 0o700 {
		return errors.New("fresh private directory is not a private mode 0700 directory")
	}
	selected, err := openDirectoryWithin(root, target)
	if err != nil {
		return fmt.Errorf("fresh private directory was replaced or redirected: %w", err)
	}
	defer selected.Close()
	selectedInfo, err := selected.Stat()
	if err != nil || !os.SameFile(heldInfo, selectedInfo) {
		return errors.New("fresh private directory was substituted after creation")
	}
	entries, readErr := selected.ReadDir(1)
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		return errors.New("fresh private directory is not empty")
	}
	return nil
}

func replaceDirectoryWithOwnedTree(ctx context.Context, root, relative, source string) error {
	parts, err := relativePathParts(root, filepath.Join(root, relative), false)
	if err != nil {
		return err
	}
	rootDirectory, err := openDirectoryAbsolute(root)
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	parent, err := openOrCreateDirectoryAt(rootDirectory, parts[:len(parts)-1])
	if err != nil {
		return err
	}
	defer parent.Close()
	sourceDirectory, err := openDirectoryAbsolute(source)
	if err != nil {
		return err
	}
	defer sourceDirectory.Close()
	temporary := ".orchard-headers-" + operationID()
	temporaryRoot := filepath.Join(root, filepath.Dir(relative), temporary)
	if err := copyOwnedTreeToParent(ctx, sourceDirectory, source, parent, temporary, temporaryRoot); err != nil {
		return err
	}
	leaf := parts[len(parts)-1]
	backup := ".orchard-replaced-" + operationID()
	hadOriginal := false
	if err := syscall.Renameat(int(parent.Fd()), leaf, int(parent.Fd()), backup); err == nil {
		hadOriginal = true
	} else if !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("confine existing Clang headers before replacement: %w", err)
	}
	if err := syscall.Renameat(int(parent.Fd()), temporary, int(parent.Fd()), leaf); err != nil {
		if hadOriginal {
			_ = syscall.Renameat(int(parent.Fd()), backup, int(parent.Fd()), leaf)
		}
		return err
	}
	verifiedFD, err := syscall.Openat(int(parent.Fd()), leaf, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("final Clang header target is not a contained real directory")
	}
	_ = syscall.Close(verifiedFD)
	if hadOriginal {
		if err := removeTreeAt(parent, backup); err != nil {
			return fmt.Errorf("remove replaced staged headers: %w", err)
		}
	}
	return nil
}

func removeTreeAt(parent *os.File, name string) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return syscall.Unlinkat(int(parent.Fd()), name)
	}
	if err != nil {
		return err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		_ = syscall.Close(fd)
		return syscall.Unlinkat(int(parent.Fd()), name)
	}
	directory := os.NewFile(uintptr(fd), descriptorPath(int(parent.Fd()), name))
	entries, err := directory.ReadDir(-1)
	if err != nil {
		directory.Close()
		return err
	}
	for _, entry := range entries {
		if err := removeTreeAt(directory, entry.Name()); err != nil {
			directory.Close()
			return err
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return syscall.Rmdir(descriptorPath(int(parent.Fd()), name))
}

func writeExclusiveFileWithin(root, relative string, data []byte, perm uint32) error {
	parts, err := relativePathParts(root, filepath.Join(root, relative), false)
	if err != nil {
		return err
	}
	rootDirectory, err := openDirectoryAbsolute(root)
	if err != nil {
		return err
	}
	defer rootDirectory.Close()
	parent, err := openOrCreateDirectoryAt(rootDirectory, parts[:len(parts)-1])
	if err != nil {
		return err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), parts[len(parts)-1], syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), relative)
	written, writeErr := io.Copy(file, bytes.NewReader(data))
	if writeErr == nil && written != int64(len(data)) {
		writeErr = io.ErrShortWrite
	}
	if syncErr := file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func findNamedRegularFilesWithin(ctx context.Context, root, wanted string, entryLimit int) ([]string, error) {
	if wanted == "" || strings.ContainsRune(wanted, filepath.Separator) {
		return nil, errors.New("invalid file name")
	}
	directory, err := openDirectoryAbsolute(root)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	results := make([]string, 0, 1)
	entries := 0
	var visit func(*os.File, string) error
	visit = func(parent *os.File, relative string) error {
		children, err := parent.ReadDir(-1)
		if err != nil {
			return err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries++
			if entries > entryLimit {
				return errors.New("tree contains too many entries")
			}
			name := child.Name()
			fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if errors.Is(err, syscall.ELOOP) {
				continue
			}
			if err != nil {
				return err
			}
			var stat syscall.Stat_t
			if err := syscall.Fstat(fd, &stat); err != nil {
				_ = syscall.Close(fd)
				return err
			}
			childRelative := filepath.Join(relative, name)
			switch stat.Mode & syscall.S_IFMT {
			case syscall.S_IFDIR:
				childDirectory := os.NewFile(uintptr(fd), childRelative)
				err = visit(childDirectory, childRelative)
				err = errors.Join(err, childDirectory.Close())
				if err != nil {
					return err
				}
			case syscall.S_IFREG:
				_ = syscall.Close(fd)
				if name == wanted {
					results = append(results, childRelative)
				}
			default:
				_ = syscall.Close(fd)
				return fmt.Errorf("tree contains a special file: %s", childRelative)
			}
		}
		return nil
	}
	if err := visit(directory, ""); err != nil {
		return nil, err
	}
	return results, nil
}
