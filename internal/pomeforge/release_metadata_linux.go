//go:build linux

package pomeforge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
)

func rewriteBundleInfoFile(project, bundle string, transform func([]byte) ([]byte, error)) error {
	directory, err := openDirectoryWithin(project, bundle)
	if err != nil {
		return errors.New("release bundle became unsafe before metadata update")
	}
	defer directory.Close()
	original, err := syscall.Openat(int(directory.Fd()), "Info.plist", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("release Info.plist is missing or unsafe")
	}
	originalFile := os.NewFile(uintptr(original), "Info.plist")
	var originalStat syscall.Stat_t
	if err := syscall.Fstat(original, &originalStat); err != nil || originalStat.Mode&syscall.S_IFMT != syscall.S_IFREG || originalStat.Size < 0 || originalStat.Size > maxManifestBytes {
		originalFile.Close()
		return errors.New("release Info.plist is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(originalFile, maxManifestBytes+1))
	closeErr := originalFile.Close()
	if err != nil || closeErr != nil || len(data) > maxManifestBytes || int64(len(data)) != originalStat.Size {
		return errors.New("release Info.plist changed or could not be read safely")
	}
	replacement, err := transform(data)
	if err != nil {
		return err
	}
	if len(replacement) > maxManifestBytes {
		return errors.New("updated release Info.plist exceeds its size limit")
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := ".Info.plist.pomeforge-" + hex.EncodeToString(random)
	temporaryFD, err := syscall.Openat(int(directory.Fd()), temporary, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, originalStat.Mode&0o777)
	if err != nil {
		return err
	}
	temporaryFile := os.NewFile(uintptr(temporaryFD), temporary)
	keep := false
	defer func() {
		_ = temporaryFile.Close()
		if !keep {
			_ = syscall.Unlinkat(int(directory.Fd()), temporary)
		}
	}()
	if _, err := temporaryFile.Write(replacement); err != nil {
		return err
	}
	if err := temporaryFile.Sync(); err != nil {
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
	currentFD, err := syscall.Openat(int(directory.Fd()), "Info.plist", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("release Info.plist changed before metadata publication")
	}
	var currentStat syscall.Stat_t
	statErr := syscall.Fstat(currentFD, &currentStat)
	_ = syscall.Close(currentFD)
	if statErr != nil || currentStat.Dev != originalStat.Dev || currentStat.Ino != originalStat.Ino {
		return errors.New("release Info.plist changed before metadata publication")
	}
	if err := syscall.Renameat(int(directory.Fd()), temporary, int(directory.Fd()), "Info.plist"); err != nil {
		return err
	}
	keep = true
	return directory.Sync()
}
