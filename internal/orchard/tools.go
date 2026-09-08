package orchard

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ToolResolver interface {
	Probe(context.Context, string) ToolStatus
	ProbeAll(context.Context) []ToolStatus
}

type toolDefinition struct {
	id, name, executable string
	versionArgs          []string
	installURL           string
}

var toolDefinitions = []toolDefinition{
	{id: "xtool", name: "xtool", executable: "xtool", versionArgs: []string{"--version"}, installURL: "https://github.com/xtool-org/xtool/releases/tag/1.19.0"},
	{id: "swift", name: "Swift", executable: "swift", versionArgs: []string{"--version"}, installURL: "https://www.swift.org/install/linux/"},
	{id: "asc", name: "ASC CLI", executable: "asc", versionArgs: []string{"--version"}, installURL: "https://github.com/rorkai/App-Store-Connect-CLI/releases/tag/5.0.0"},
	{id: "zsign", name: "zsign", executable: "zsign", versionArgs: []string{"--version"}, installURL: "https://github.com/zhlynn/zsign/releases/tag/v1.1.2"},
	{id: "idevice_id", name: "libimobiledevice", executable: "idevice_id", versionArgs: []string{"--version"}, installURL: "https://libimobiledevice.org/"},
	{id: "usbmuxd", name: "usbmuxd", executable: "usbmuxd", versionArgs: []string{"--version"}, installURL: "https://github.com/libimobiledevice/usbmuxd"},
}

type SystemToolResolver struct {
	Timeout time.Duration
}

func (r SystemToolResolver) ProbeAll(ctx context.Context) []ToolStatus {
	result := make([]ToolStatus, len(toolDefinitions))
	var group sync.WaitGroup
	for index, definition := range toolDefinitions {
		group.Add(1)
		go func() {
			defer group.Done()
			result[index] = r.probeDefinition(ctx, definition)
		}()
	}
	group.Wait()
	return result
}

func (r SystemToolResolver) Probe(ctx context.Context, id string) ToolStatus {
	for _, definition := range toolDefinitions {
		if definition.id == id {
			return r.probeDefinition(ctx, definition)
		}
	}
	return ToolStatus{ID: id, Name: id, Status: "missing", Detail: "unknown tool adapter"}
}

func (r SystemToolResolver) probeDefinition(ctx context.Context, definition toolDefinition) ToolStatus {
	status := ToolStatus{ID: definition.id, Name: definition.name, Status: "missing", Detail: "executable not found on PATH", InstallURL: definition.installURL}
	path, err := exec.LookPath(definition.executable)
	if err != nil {
		return status
	}
	if absolute, absErr := filepath.Abs(path); absErr == nil {
		path = absolute
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	status.Path = path
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var output cappedBuffer
	output.limit = 4096
	cmd := exec.CommandContext(probeCtx, path, definition.versionArgs...)
	cmd.Env = ChildEnvironmentFor(EnvironmentProbe)
	cmd.Stdout = &output
	cmd.Stderr = &output
	configureProcessGroup(cmd)
	err = cmd.Run()
	killProcessGroup(cmd)
	version := strings.TrimSpace(redactOutput(output.String()))
	version = strings.Join(strings.Fields(version), " ")
	if len(version) > 240 {
		version = version[:240] + "…"
	}
	status.Version = version
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		status.Status = "unverified"
		status.Detail = "version probe timed out"
		return status
	}
	if err != nil {
		status.Status = "unverified"
		status.Detail = "executable was found but its version probe failed"
		return status
	}
	status.Status = "available"
	status.Detail = "verified by executing " + definition.executable + " " + strings.Join(definition.versionArgs, " ")
	if definition.id == "xtool" && !regexp.MustCompile(`(?i)^xtool\s+1\.19\.0(?:\s|$)`).MatchString(version) {
		status.Status = "unverified"
		status.Detail = "Orchard has verified only xtool 1.19.0 command contracts; this version is not verified"
	}
	if definition.id == "asc" && !regexp.MustCompile(`(?i)(?:asc[^0-9]*)?5\.[0-9]+`).MatchString(version) {
		status.Status = "incompatible"
		status.Detail = "Orchard has verified ASC 5.x command contracts; found a different version"
	}
	if definition.id == "swift" {
		matches := regexp.MustCompile(`(?i)Swift version\s+([0-9]+)\.([0-9]+)`).FindStringSubmatch(version)
		if len(matches) != 3 {
			status.Status = "unverified"
			status.Detail = "Swift ran, but Orchard could not parse its version"
		} else {
			major, _ := strconv.Atoi(matches[1])
			minor, _ := strconv.Atoi(matches[2])
			if major < 6 || (major == 6 && minor < 3) {
				status.Status = "incompatible"
				status.Detail = "xtool 1.19.0 requires Swift 6.3 or newer"
			}
		}
	}
	return status
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if b.limit <= 0 {
		b.limit = 64 * 1024
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		b.truncated = true
		return original, nil
	}
	_, _ = b.buffer.Write(p)
	return original, nil
}

func (b *cappedBuffer) String() string {
	value := b.buffer.String()
	if b.truncated {
		value += "\n[output truncated by Orchard]\n"
	}
	return value
}
