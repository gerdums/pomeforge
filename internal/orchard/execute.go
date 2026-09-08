package orchard

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	EnvironmentProbe      = "probe"
	EnvironmentASC        = "asc"
	EnvironmentBrowser    = "browser"
	maxHistoryBytes       = 8 << 20
	maxRedactionLookahead = 1 << 20
)

type Executor struct {
	Timeout       time.Duration
	OutputCap     int
	HostOS        string
	Workspace     string
	XDGConfigHome string

	mu      sync.Mutex
	running map[string]bool
}

func ChildEnvironment() []string {
	return ChildEnvironmentFor("")
}

func ChildEnvironmentFor(adapter string) []string {
	allowedExact := map[string]bool{
		"HOME": true, "PATH": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true,
		"NO_PROXY": true, "http_proxy": true, "https_proxy": true, "no_proxy": true,
	}
	allowedPrefixes := []string{"XDG_"}
	if adapter != EnvironmentProbe && adapter != EnvironmentBrowser && adapter != EnvironmentASC {
		allowedPrefixes = append(allowedPrefixes, "SWIFT_")
	}
	if adapter == EnvironmentASC {
		allowedPrefixes = append(allowedPrefixes, "ASC_")
	}
	if adapter == EnvironmentBrowser {
		for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "DESKTOP_SESSION", "XDG_CURRENT_DESKTOP", "XDG_SESSION_DESKTOP", "XDG_SESSION_TYPE", "XDG_RUNTIME_DIR"} {
			allowedExact[key] = true
		}
	}
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		allowed := allowedExact[key]
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(key, prefix) {
				allowed = true
			}
		}
		if allowed {
			values[key] = value
		}
	}
	if adapter == EnvironmentASC {
		values["ASC_TELEMETRY_DISABLED"] = "1"
	}
	values["DO_NOT_TRACK"] = "1"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func environmentWithXDGConfigHome(adapter, configHome string) []string {
	environment := ChildEnvironmentFor(adapter)
	if configHome == "" {
		return environment
	}
	prefix := "XDG_CONFIG_HOME="
	filtered := environment[:0]
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			filtered = append(filtered, value)
		}
	}
	return append(filtered, prefix+configHome)
}

func effectiveXDGConfigHome() (string, error) {
	if configured := os.Getenv("XDG_CONFIG_HOME"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("XDG_CONFIG_HOME must be absolute for Swift SDK persistence")
		}
		clean := filepath.Clean(configured)
		if clean == string(filepath.Separator) {
			return "", errors.New("XDG_CONFIG_HOME may not be the filesystem root")
		}
		return clean, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("resolve an absolute user home for Swift SDK persistence")
	}
	return filepath.Join(home, ".config"), nil
}

func (e *Executor) Execute(ctx context.Context, plan Plan, project string, confirm bool) (OperationResult, error) {
	hostOS := e.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	if actionRequiresLinux(plan.Action) && hostOS != "linux" {
		return OperationResult{}, Errorf("blocked", "this compile, signing, or export workflow is supported only when Orchard is running on Linux")
	}
	if !plan.Executable {
		return OperationResult{}, Errorf("blocked", "operation is blocked: "+strings.Join(plan.Blockers, "; "))
	}
	if plan.RequiresConfirmation && !confirm {
		return OperationResult{}, Errorf("confirmation_required", "this operation has an account, signing, or external-write effect and requires --confirm")
	}
	key := project + "\x00" + plan.Action
	e.mu.Lock()
	if e.running == nil {
		e.running = make(map[string]bool)
	}
	if e.running[key] {
		e.mu.Unlock()
		return OperationResult{}, Errorf("operation_in_progress", "the same operation is already running for this project")
	}
	e.running[key] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, key)
		e.mu.Unlock()
	}()
	workspace := e.Workspace
	if workspace == "" {
		workspace = project
	}
	if err := ensureHistory(workspace, project); err != nil {
		return OperationResult{}, Errorf("history_failed", "operation was not started because private history is unavailable: "+err.Error())
	}

	started := time.Now().UTC()
	result := OperationResult{ID: operationID(), Action: plan.Action, Status: "succeeded", ExitCode: 0, StartedAt: started, Scope: "project", Project: plan.Project, ProjectLabel: plan.ProjectLabel}
	output := &cappedBuffer{limit: e.OutputCap}
	if output.limit <= 0 {
		output.limit = 64 * 1024
	}
	for _, step := range plan.Steps {
		if output.buffer.Len() > 0 {
			_, _ = output.Write([]byte("\n"))
		}
		_, _ = output.Write([]byte("[" + step.Tool + "] " + step.Description + "\n"))
		timeout := e.Timeout
		if timeout <= 0 {
			timeout = 15 * time.Minute
		}
		stepCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(stepCtx, step.Executable, step.Args...)
		cmd.Dir = step.Directory
		cmd.Env = environmentWithXDGConfigHome(step.Tool, e.XDGConfigHome)
		cmd.Stdout = output
		cmd.Stderr = output
		configureProcessGroup(cmd)
		err := cmd.Run()
		killProcessGroup(cmd)
		cancel()
		if errors.Is(stepCtx.Err(), context.DeadlineExceeded) {
			result.Status = "failed"
			result.ExitCode = 124
			_, _ = output.Write([]byte("\noperation timed out\n"))
			break
		}
		if err != nil {
			result.Status = "failed"
			result.ExitCode = 127
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				result.ExitCode = exitErr.ExitCode()
			}
			_, _ = output.Write([]byte(fmt.Sprintf("\nprocess failed: %s\n", safeProcessError(err))))
			break
		}
	}
	result.FinishedAt = time.Now().UTC()
	result.Output = output.String()
	if err := appendHistory(workspace, project, result); err != nil {
		message := "operation completed, but its private history receipt could not be stored: " + err.Error()
		return result, ErrorWithResult("history_failed", message, result)
	}
	return result, nil
}

func safeProcessError(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("exit status %d", exitErr.ExitCode())
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "executable not found"
	}
	return "could not start executable"
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|\z)`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(private[-_ ]?key\s*[:=]\s*)[^\s]+`),
	regexp.MustCompile(`(?i)(token\s*[:=]\s*)[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`(?i)(https?://[^\s?]+)\?[^\s]+`),
}

func redactOutput(value string) string {
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllString(value, "${1}[redacted]")
	}
	known := credentialEnvironmentValues()
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	for _, secret := range known {
		value = strings.ReplaceAll(value, secret, "[redacted]")
		maximum := len(secret) - 1
		if maximum > len(value) {
			maximum = len(value)
		}
		for length := maximum; length >= 4; length-- {
			if strings.HasSuffix(value, secret[:length]) {
				value = value[:len(value)-length] + "[redacted]"
				break
			}
		}
	}
	return value
}

func credentialEnvironmentValues() []string {
	values := make([]string, 0)
	seen := map[string]bool{}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		sensitive := strings.HasPrefix(upper, "ASC_") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "PRIVATE_KEY")
		if found && sensitive && len(value) >= 4 && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func operationID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func appendHistory(workspace, project string, result OperationResult) error {
	file, err := openHistoryWithin(workspace, project, syscall.O_WRONLY|syscall.O_APPEND, true)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = file.Write(encoded)
	return err
}

func ensureHistory(workspace, project string) error {
	file, err := openHistoryWithin(workspace, project, syscall.O_WRONLY|syscall.O_APPEND, true)
	if err != nil {
		return err
	}
	return file.Close()
}

func LoadHistory(project string, limit int) ([]OperationResult, error) {
	return loadHistoryWithin(project, project, limit)
}

func loadHistoryWithin(workspace, project string, limit int) ([]OperationResult, error) {
	file, err := openHistoryWithin(workspace, project, syscall.O_RDONLY, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return []OperationResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxHistoryBytes {
		return nil, fmt.Errorf("history.jsonl exceeds the %d-byte read limit", maxHistoryBytes)
	}
	return scanHistory(file, limit)
}

func scanHistory(reader io.Reader, limit int) ([]OperationResult, error) {
	if limit <= 0 {
		limit = 100
	}
	bounded := &io.LimitedReader{R: reader, N: maxHistoryBytes + 1}
	results := make([]OperationResult, 0)
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		var result OperationResult
		if json.Unmarshal(scanner.Bytes(), &result) == nil {
			// History may have been written by an older Orchard redactor. Apply
			// the current policy again before exposing retained output to callers.
			result.Output = redactOutput(result.Output)
			results = append(results, result)
			if len(results) > limit {
				results = results[1:]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if bounded.N == 0 {
		return nil, fmt.Errorf("history.jsonl exceeds the %d-byte read limit", maxHistoryBytes)
	}
	for left, right := 0, len(results)-1; left < right; left, right = left+1, right-1 {
		results[left], results[right] = results[right], results[left]
	}
	return results, nil
}

func openHistoryWithin(workspace, project string, flags int, create bool) (*os.File, error) {
	projectDirectory, err := openDirectoryWithin(workspace, project)
	if err != nil {
		return nil, err
	}
	defer projectDirectory.Close()
	projectFD := int(projectDirectory.Fd())
	if create {
		if err := syscall.Mkdirat(projectFD, ".orchard", 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, err
		}
	}
	directoryFD, err := syscall.Openat(projectFD, ".orchard", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf(".orchard must be a private directory and not a symlink: %w", err)
	}
	defer syscall.Close(directoryFD)
	if err := syscall.Fchmod(directoryFD, 0o700); err != nil {
		return nil, err
	}
	openFlags := flags | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	fileFD, err := syscall.Openat(directoryFD, "history.jsonl", openFlags, 0)
	if create && errors.Is(err, syscall.ENOENT) {
		fileFD, err = syscall.Openat(directoryFD, "history.jsonl", openFlags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
		if errors.Is(err, syscall.EEXIST) {
			fileFD, err = syscall.Openat(directoryFD, "history.jsonl", openFlags, 0)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("history.jsonl must be a regular file and not a symlink: %w", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fileFD, &stat); err != nil {
		_ = syscall.Close(fileFD)
		return nil, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fileFD)
		return nil, errors.New("history.jsonl must be a regular file")
	}
	if err := syscall.Fchmod(fileFD, 0o600); err != nil {
		_ = syscall.Close(fileFD)
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), filepath.Join(project, ".orchard", "history.jsonl")), nil
}
