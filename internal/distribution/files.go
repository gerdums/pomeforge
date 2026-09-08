package distribution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func readRegularFile(ctx context.Context, name string, max int64) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", name)
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%q is %d bytes; limit is %d", name, info.Size(), max)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%q changed during safe open", name)
	}
	b, err := readBounded(ctx, f, max)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", name, err)
	}
	return b, nil
}

func readBounded(ctx context.Context, r io.Reader, max int64) ([]byte, error) {
	if max < 0 {
		return nil, errors.New("negative read limit")
	}
	lr := &io.LimitedReader{R: r, N: max + 1}
	buf := make([]byte, 0, min(max, 64<<10))
	tmp := make([]byte, 32<<10)
	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		n, err := lr.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if int64(len(buf)) > max {
				return nil, fmt.Errorf("content exceeds %d-byte limit", max)
			}
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func requireAbsoluteCleanPath(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be an absolute clean path", field)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains NUL", field)
	}
	return nil
}

func rejectSymlinkPath(path string, includeLeaf bool) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.TrimPrefix(clean, volume)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(rest, string(filepath.Separator)), string(filepath.Separator))
	if !includeLeaf && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path traverses symlink %q", current)
		}
	}
	return nil
}

func samePublicKey(a, b any) bool {
	return publicKeyFingerprint(a) == publicKeyFingerprint(b) && publicKeyFingerprint(a) != ""
}
