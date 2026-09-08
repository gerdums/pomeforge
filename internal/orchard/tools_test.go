package orchard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeScript(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+contents+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCapturedToolVersionClassification(t *testing.T) {
	tests := []struct {
		name, id, output, wantStatus, wantDetail string
	}{
		{"swift-6.3", "swift", "Swift version 6.3 (swift-6.3-RELEASE)\\nTarget: aarch64-unknown-linux-gnu", "available", "verified by executing"},
		{"swift-old", "swift", "Swift version 6.2.3 (swift-6.2.3-RELEASE)", "incompatible", "requires Swift 6.3"},
		{"asc-5", "asc", "asc version 5.0.0 (commit synthetic)", "available", "verified by executing"},
		{"asc-old", "asc", "asc version 4.2.0", "incompatible", "verified ASC 5.x"},
		{"asc-wrong-prefix", "asc", "tool 5.0.0", "incompatible", "verified ASC 5.x"},
		{"asc-future-major", "asc", "asc version 15.0.0", "incompatible", "verified ASC 5.x"},
		{"xtool-pinned", "xtool", "xtool 1.19.0", "available", "verified by executing"},
		{"xtool-old", "xtool", "xtool 1.18.0", "unverified", "only xtool 1.19.0"},
		{"xtool-unknown", "xtool", "unexpected version text", "unverified", "only xtool 1.19.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := probeScript(t, "printf '"+test.output+"\\n'")
			definition := toolDefinition{id: test.id, name: test.id, executable: path, versionArgs: []string{"--version"}, installURL: "https://example.invalid"}
			status := (SystemToolResolver{Timeout: time.Second}).probeDefinition(context.Background(), definition)
			if status.Status != test.wantStatus || !strings.Contains(status.Detail, test.wantDetail) {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestToolProbeKillsPipeHoldingDescendantAndRedactsDiagnostics(t *testing.T) {
	path := probeScript(t, "(sleep 5) &\nexit 0")
	definition := toolDefinition{id: "zsign", name: "zsign", executable: path, versionArgs: []string{"--version"}, installURL: "https://example.invalid"}
	started := time.Now()
	status := (SystemToolResolver{Timeout: 100 * time.Millisecond}).probeDefinition(context.Background(), definition)
	if time.Since(started) > 2*time.Second {
		t.Fatalf("pipe-holding probe was not bounded: %s", time.Since(started))
	}
	if status.Status != "unverified" {
		t.Fatalf("pipe-holding probe status = %#v", status)
	}

	canary := "synthetic-probe-credential"
	t.Setenv("ASC_KEY_CANARY", canary)
	path = probeScript(t, "printf '"+canary+"\\n'")
	definition.executable = path
	status = (SystemToolResolver{Timeout: time.Second}).probeDefinition(context.Background(), definition)
	if strings.Contains(status.Version, canary) || !strings.Contains(status.Version, "[redacted]") {
		t.Fatalf("probe diagnostic was not redacted: %#v", status)
	}
}

func TestToolProbeRedactsPEMBeforeTruncation(t *testing.T) {
	path := probeScript(t, "printf '%b' '-----BEGIN PRIVATE KEY-----\\n"+strings.Repeat("synthetic-private-material", 400)+"\\n-----END PRIVATE KEY-----'")
	definition := toolDefinition{id: "zsign", name: "zsign", executable: path, versionArgs: []string{"--version"}, installURL: "https://example.invalid"}
	status := (SystemToolResolver{Timeout: time.Second}).probeDefinition(context.Background(), definition)
	if strings.Contains(status.Version, "BEGIN PRIVATE KEY") || strings.Contains(status.Version, "synthetic-private-material") || !strings.Contains(status.Version, "[redacted]") {
		t.Fatalf("truncated probe output was not redacted: %#v", status)
	}
}
