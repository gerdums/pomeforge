package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const SigningConfigVersion = 1

// SigningConfig references private identity material without containing it.
// Callers must keep this configuration and every referenced file outside
// project trees.
type SigningConfig struct {
	Version                 int               `json:"version"`
	PrivateKeyPath          string            `json:"privateKeyPath"`
	CertificatePath         string            `json:"certificatePath"`
	ProvisioningProfilePath string            `json:"provisioningProfilePath"`
	TrustedRootPaths        []string          `json:"trustedRootPaths,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

func validateConfig(c SigningConfig) error {
	if c.Version != SigningConfigVersion {
		return fmt.Errorf("unsupported signing config version %d", c.Version)
	}
	paths := []struct{ field, value string }{
		{"privateKeyPath", c.PrivateKeyPath},
		{"certificatePath", c.CertificatePath},
		{"provisioningProfilePath", c.ProvisioningProfilePath},
	}
	if len(c.TrustedRootPaths) > 16 {
		return fmt.Errorf("trustedRootPaths has %d entries; limit is 16", len(c.TrustedRootPaths))
	}
	for i, p := range c.TrustedRootPaths {
		paths = append(paths, struct{ field, value string }{fmt.Sprintf("trustedRootPaths[%d]", i), p})
	}
	for _, p := range paths {
		if err := requireAbsoluteCleanPath(p.field, p.value); err != nil {
			return err
		}
	}
	if len(c.Metadata) > 32 {
		return fmt.Errorf("metadata has %d entries; limit is 32", len(c.Metadata))
	}
	for k, v := range c.Metadata {
		if strings.TrimSpace(k) == "" || len(k) > 64 || len(v) > 512 {
			return fmt.Errorf("metadata keys must be 1-64 bytes and values at most 512 bytes")
		}
	}
	return nil
}

// CreateSigningConfig writes a new 0600 regular file. It never creates parent
// directories, follows symlinks, or replaces an existing path.
func CreateSigningConfig(ctx context.Context, path string, c SigningConfig) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := requireAbsoluteCleanPath("config path", path); err != nil {
		return err
	}
	if err := validateConfig(c); err != nil {
		return err
	}
	if err := rejectSymlinkPath(path, false); err != nil {
		return fmt.Errorf("validate config parent: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return fmt.Errorf("config parent is not a directory")
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if int64(len(b)) > DefaultLimits().MaxConfigBytes {
		return fmt.Errorf("encoded config exceeds size limit")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

// LoadSigningConfig strictly loads a bounded 0600 regular JSON file.
func LoadSigningConfig(ctx context.Context, path string, limits Limits) (SigningConfig, error) {
	var c SigningConfig
	limits = limits.withDefaults()
	if err := requireAbsoluteCleanPath("config path", path); err != nil {
		return c, err
	}
	if err := rejectSymlinkPath(path, true); err != nil {
		return c, fmt.Errorf("validate config path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() {
		return c, fmt.Errorf("config is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return c, fmt.Errorf("config permissions %04o expose private configuration; require 0600 or stricter", info.Mode().Perm())
	}
	b, err := readRegularFile(ctx, path, limits.MaxConfigBytes)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode signing config: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return c, fmt.Errorf("decode signing config: trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("decode signing config: trailing data: %w", err)
	}
	if err := validateConfig(c); err != nil {
		return c, err
	}
	return c, nil
}
