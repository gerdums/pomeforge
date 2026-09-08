package distribution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func validConfigFor(dir string) SigningConfig {
	return SigningConfig{Version: 1, PrivateKeyPath: filepath.Join(dir, "key"), CertificatePath: filepath.Join(dir, "cert"), ProvisioningProfilePath: filepath.Join(dir, "profile")}
}

func TestConfigStrictPrivateCreateLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")
	c := validConfigFor(dir)
	if err := CreateSigningConfig(context.Background(), path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	loaded, err := LoadSigningConfig(context.Background(), path, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrivateKeyPath != c.PrivateKeyPath {
		t.Fatalf("loaded %+v", loaded)
	}
	if err := CreateSigningConfig(context.Background(), path, c); err == nil {
		t.Fatal("existing config was overwritten")
	}
}

func TestConfigRejectsSymlinksUnknownFieldsAndBroadPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writePrivate(t, target, []byte(`{"version":1}`))
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningConfig(context.Background(), link, Limits{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error=%v", err)
	}
	unknown := filepath.Join(dir, "unknown.json")
	writePrivate(t, unknown, []byte(`{"version":1,"privateKeyPath":"/a","certificatePath":"/b","provisioningProfilePath":"/c","surprise":true}`))
	if _, err := LoadSigningConfig(context.Background(), unknown, Limits{}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error=%v", err)
	}
	broad := filepath.Join(dir, "broad.json")
	writePrivate(t, broad, []byte(mustJSON(t, validConfigFor(dir))))
	if err := os.Chmod(broad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningConfig(context.Background(), broad, Limits{}); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("permission error=%v", err)
	}
}

func TestConfigBoundedAndContextAware(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.json")
	writePrivate(t, path, make([]byte, 1024))
	if _, err := LoadSigningConfig(context.Background(), path, Limits{MaxConfigBytes: 100}); err == nil {
		t.Fatal("oversized config accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadSigningConfig(ctx, path, Limits{MaxConfigBytes: 2048}); err == nil {
		t.Fatal("canceled read accepted")
	}
}
