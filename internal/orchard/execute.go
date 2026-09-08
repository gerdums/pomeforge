package orchard

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Executor struct {
	Timeout   time.Duration
	OutputCap int

	mu      sync.Mutex
	running map[string]bool
}

func ChildEnvironment() []string {
	allowedExact := map[string]bool{
		"HOME": true, "PATH": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true,
		"NO_PROXY": true, "http_proxy": true, "https_proxy": true, "no_proxy": true,
	}
	allowedPrefixes := []string{"XDG_", "ASC_", "SWIFT_"}
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
	values["ASC_TELEMETRY_DISABLED"] = "1"
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

func (e *Executor) Execute(ctx context.Context, plan Plan, project string, confirm bool) (OperationResult, error) {
	if !plan.Executable {
		return OperationResult{}, Errorf("blocked", "operation is blocked: "+strings.Join(plan.Blockers, "; "))
	}
	if plan.RequiresConfirmation && !confirm {
		return OperationResult{}, Errorf("confirmation_required", "this account write requires --confirm")
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
	if err := ensureHistory(project); err != nil {
		return OperationResult{}, Errorf("history_failed", "operation was not started because private history is unavailable: "+err.Error())
	}

	started := time.Now().UTC()
	result := OperationResult{ID: operationID(), Action: plan.Action, Status: "succeeded", ExitCode: 0, StartedAt: started}
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
		cmd.Env = ChildEnvironment()
		cmd.Stdout = output
		cmd.Stderr = output
		err := cmd.Run()
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
	result.Output = redactOutput(output.String())
	if err := appendHistory(project, result); err != nil {
		return result, Errorf("history_failed", "operation completed but history could not be stored: "+err.Error())
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
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(private[-_ ]?key\s*[:=]\s*)[^\s]+`),
	regexp.MustCompile(`(?i)(token\s*[:=]\s*)[A-Za-z0-9._~+/=-]{8,}`),
}

func redactOutput(value string) string {
	for _, pattern := range secretPatterns {
		value = pattern.ReplaceAllString(value, "${1}[redacted]")
	}
	return value
}

func operationID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func appendHistory(project string, result OperationResult) error {
	if err := ensureHistory(project); err != nil {
		return err
	}
	path := filepath.Join(project, ".orchard", "history.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
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

func ensureHistory(project string) error {
	directory := filepath.Join(project, ".orchard")
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New(".orchard must not be a symlink")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, "history.jsonl")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("history.jsonl must not be a symlink")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	return nil
}

func LoadHistory(project string, limit int) ([]OperationResult, error) {
	directory := filepath.Join(project, ".orchard")
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New(".orchard must not be a symlink")
	}
	path := filepath.Join(project, ".orchard", "history.jsonl")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("history.jsonl must not be a symlink")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []OperationResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if limit <= 0 {
		limit = 100
	}
	results := make([]OperationResult, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		var result OperationResult
		if json.Unmarshal(scanner.Bytes(), &result) == nil {
			results = append(results, result)
			if len(results) > limit {
				results = results[1:]
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(results)-1; left < right; left, right = left+1, right-1 {
		results[left], results[right] = results[right], results[left]
	}
	return results, nil
}
