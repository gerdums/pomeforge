package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pomeforge.local/pomeforge/internal/pomeforge"
)

type webTools map[string]pomeforge.ToolStatus

func (f webTools) Probe(_ context.Context, id string) pomeforge.ToolStatus {
	if status, ok := f[id]; ok {
		return status
	}
	return pomeforge.ToolStatus{ID: id, Name: id, Status: "missing", Detail: "missing", InstallURL: "https://example.invalid"}
}

func (f webTools) ProbeAll(ctx context.Context) []pomeforge.ToolStatus {
	result := make([]pomeforge.ToolStatus, 0)
	for _, id := range []string{"xtool", "swift", "asc", "zsign", "idevice_id", "usbmuxd"} {
		result = append(result, f.Probe(ctx, id))
	}
	return result
}

func testAPI(t *testing.T) (*httptest.Server, *API, string) {
	t.Helper()
	workspace := t.TempDir()
	executable := filepath.Join(t.TempDir(), "tool-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := webTools{}
	for _, id := range []string{"xtool", "swift", "asc"} {
		tools[id] = pomeforge.ToolStatus{ID: id, Name: id, Status: "available", Version: "test", Path: executable, Detail: "test"}
	}
	service, err := pomeforge.NewService(workspace, tools)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Service: service, Token: "test-token"}
	server := httptest.NewServer(api.Handler())
	api.AllowedOrigin = server.URL
	t.Cleanup(server.Close)
	return server, api, workspace
}

func request(t *testing.T, server *httptest.Server, method, route, body, token, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAPITokenHostOriginAndRoot(t *testing.T) {
	server, _, _ := testAPI(t)
	response := request(t, server, http.MethodGet, "/", "", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodGet, "/api/state", "", "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodGet, "/api/state", "", "test-token", "https://evil.example")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/state", nil)
	req.Host = "evil.example"
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/state", nil)
	req.Host = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer test-token")
	response, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched loopback port status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/state", nil)
	req.Host = "localhost:" + strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	req.Header.Set("Authorization", "Bearer test-token")
	response, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("deliberate loopback alias status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodGet, "/api/state", "", "test-token", server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d", response.StatusCode)
	}
	if response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("permissive CORS header present")
	}
	_ = response.Body.Close()
}

func TestAPIPostProtectionsAndWorkspacePaths(t *testing.T) {
	server, _, workspace := testAPI(t)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/projects", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("content type status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/projects", `{"name":"Demo","bundleId":"com.example.Demo","directory":"Demo","unknown":true}`, "test-token", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/projects", `{"name":"Demo","bundleId":"com.example.Demo","directory":"../Demo"}`, "test-token", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/plan", `{"action":"build","project":"Demo","executable":"/bin/true"}`, "test-token", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("browser argv status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/projects", `{"name":"Demo","bundleId":"com.example.Demo","directory":"`+filepath.Join(workspace, "Demo")+`"}`, "test-token", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("absolute API path status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	oversized := `{"name":"` + strings.Repeat("x", maxRequestBody) + `"}`
	response = request(t, server, http.MethodPost, "/api/projects", oversized, "test-token", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
}

func TestAPIProjectPlanAndConfirmationGate(t *testing.T) {
	server, _, workspace := testAPI(t)
	response := request(t, server, http.MethodPost, "/api/projects", `{"name":"Demo","bundleId":"com.example.Demo","directory":"Demo"}`, "test-token", "")
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create status = %d: %s", response.StatusCode, body)
	}
	_ = response.Body.Close()
	manifest, _, err := pomeforge.LoadManifest(filepath.Join(workspace, "Demo"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.AppStore.AppID = "1001"
	manifest.AppStore.VersionID = "a1b2c3d4-1111-4222-8333-abcdef123456"
	manifest.AppStore.BuildID = "b2c3d4e5-2222-4333-8444-bcdefa234567"
	encoded, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(workspace, "Demo", "pomeforge.json"), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPost, "/api/plan", `{"action":"submit","project":"Demo"}`, "test-token", "")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("plan status = %d: %s", response.StatusCode, body)
	}
	var wrapped struct {
		OK   bool           `json:"ok"`
		Data pomeforge.Plan `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&wrapped); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !wrapped.Data.Executable || !wrapped.Data.RequiresConfirmation {
		t.Fatalf("unexpected plan: %#v", wrapped.Data)
	}
	response = request(t, server, http.MethodPost, "/api/run", `{"planId":"`+wrapped.Data.ID+`","confirm":false}`, "test-token", "")
	if response.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("confirmation status = %d: %s", response.StatusCode, body)
	}
	_ = response.Body.Close()
}

func TestAPISigningPathsAreAcceptedButNeverReturned(t *testing.T) {
	server, _, _ := testAPI(t)
	privateRoot := t.TempDir()
	privateKey := filepath.Join(privateRoot, "missing-sensitive-name.key")
	certificate := filepath.Join(privateRoot, "certificate.pem")
	profile := filepath.Join(privateRoot, "profile.mobileprovision")
	body := fmt.Sprintf(`{"action":"signing-configure","identity":"release-main","identityLabel":"Release main","privateKeyPath":%q,"certificatePath":%q,"profilePath":%q}`, privateKey, certificate, profile)
	response := request(t, server, http.MethodPost, "/api/plan", body, "test-token", "")
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, data)
	}
	for _, privatePath := range []string{privateKey, certificate, profile} {
		if bytes.Contains(data, []byte(privatePath)) {
			t.Fatalf("API error leaked private identity path %q: %s", privatePath, data)
		}
	}
	if !bytes.Contains(data, []byte("[private-path]")) {
		t.Fatalf("API error omitted actionable redaction marker: %s", data)
	}
}

func TestAPIRejectsStaleStoredPlan(t *testing.T) {
	server, _, workspace := testAPI(t)
	project, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodPost, "/api/plan", `{"action":"build","project":"Demo"}`, "test-token", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d", response.StatusCode)
	}
	var wrapped struct {
		Data pomeforge.Plan `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&wrapped); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	source := filepath.Join(project.Path, "Sources", "Demo", "DemoApp.swift")
	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\n// changed after inspection\n")
	_ = file.Close()
	response = request(t, server, http.MethodPost, "/api/run", `{"planId":"`+wrapped.Data.ID+`","confirm":false}`, "test-token", "")
	if response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("stale status = %d: %s", response.StatusCode, body)
	}
	_ = response.Body.Close()
}

func TestAPIReturnsRedactedFailedOperationResult(t *testing.T) {
	server, api, workspace := testAPI(t)
	if _, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo"); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(workspace, "xtool-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'token=syntheticsecrettoken'\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := api.Service.Tools.(webTools)
	tools["xtool"] = pomeforge.ToolStatus{ID: "xtool", Name: "xtool", Status: "available", Path: executable, Detail: "fixture"}
	response := request(t, server, http.MethodPost, "/api/plan", `{"action":"devices","project":"Demo"}`, "test-token", "")
	var planned struct {
		Data pomeforge.Plan `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&planned); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/run", `{"planId":"`+planned.Data.ID+`","confirm":false}`, "test-token", "")
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"status":"failed"`)) || !bytes.Contains(body, []byte(`"exitCode":7`)) {
		t.Fatalf("failed operation response status=%d body=%s", response.StatusCode, body)
	}
	if bytes.Contains(body, []byte("syntheticsecrettoken")) || !bytes.Contains(body, []byte("[redacted]")) {
		t.Fatalf("failed output was not redacted: %s", body)
	}
}

func TestAPIHistoryFailureCarriesCompletedOperationResult(t *testing.T) {
	server, api, workspace := testAPI(t)
	if _, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo"); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(workspace, "history-fixture")
	script := "#!/bin/sh\nrm .pomeforge/history.jsonl && mkdir .pomeforge/history.jsonl\nprintf 'completed API output token=syntheticreceiptsecret'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := api.Service.Tools.(webTools)
	tools["xtool"] = pomeforge.ToolStatus{ID: "xtool", Name: "xtool", Status: "available", Path: executable, Detail: "fixture"}
	response := request(t, server, http.MethodPost, "/api/plan", `{"action":"devices","project":"Demo"}`, "test-token", "")
	var planned struct {
		Data pomeforge.Plan `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&planned); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response = request(t, server, http.MethodPost, "/api/run", `{"planId":"`+planned.Data.ID+`","confirm":false}`, "test-token", "")
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError || !bytes.Contains(body, []byte(`"code":"history_failed"`)) || !bytes.Contains(body, []byte(`"result":{"id"`)) || !bytes.Contains(body, []byte("completed API output")) || bytes.Contains(body, []byte("syntheticreceiptsecret")) || !bytes.Contains(body, []byte("[redacted]")) {
		t.Fatalf("history failure response status=%d body=%s", response.StatusCode, body)
	}
}

func TestServeAppPrintsActualTokenURLAndRejectsForeignBind(t *testing.T) {
	service, err := pomeforge.NewService(t.TempDir(), webTools{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ServeApp(context.Background(), service, AppOptions{Listen: "0.0.0.0:0"}); err == nil {
		t.Fatal("expected foreign bind rejection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeApp(ctx, service, AppOptions{Listen: "127.0.0.1:0", Output: writer})
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "Pomeforge app: http://127.0.0.1:") || !strings.Contains(line, "/#token=") {
		t.Fatalf("unexpected app line %q", line)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
	_ = writer.Close()
	_ = reader.Close()

	jsonContext, jsonCancel := context.WithCancel(context.Background())
	jsonReader, jsonWriter := io.Pipe()
	jsonDone := make(chan error, 1)
	go func() {
		jsonDone <- ServeApp(jsonContext, service, AppOptions{Listen: "127.0.0.1:0", JSON: true, Output: jsonWriter})
	}()
	jsonLine, err := bufio.NewReader(jsonReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var startup struct {
		OK   bool `json:"ok"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(jsonLine), &startup); err != nil || !startup.OK || !strings.Contains(startup.Data.URL, "/#token=") {
		t.Fatalf("unexpected JSON startup %q: %v", jsonLine, err)
	}
	jsonCancel()
	select {
	case err := <-jsonDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("JSON server did not shut down")
	}
	_ = jsonWriter.Close()
	_ = jsonReader.Close()
}

func TestServeAppBrowserOpenerEnvironment(t *testing.T) {
	workspace := t.TempDir()
	service, err := pomeforge.NewService(workspace, webTools{})
	if err != nil {
		t.Fatal(err)
	}
	toolsDirectory := t.TempDir()
	environmentFile := filepath.Join(t.TempDir(), "opener-environment")
	opener := filepath.Join(toolsDirectory, "xdg-open")
	// Publish only the completed environment: shell redirection creates an empty
	// file before env writes, so its mere existence is not a completion signal.
	if err := os.WriteFile(opener, []byte("#!/bin/sh\nenv > '"+environmentFile+".tmp' && mv '"+environmentFile+".tmp' '"+environmentFile+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", toolsDirectory+":/usr/bin:/bin")
	t.Setenv("DISPLAY", ":42")
	t.Setenv("WAYLAND_DISPLAY", "wayland-42")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/synthetic-bus")
	t.Setenv("XDG_CURRENT_DESKTOP", "synthetic-desktop")
	t.Setenv("ASC_PRIVATE_KEY", "synthetic-account-secret")
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeApp(ctx, service, AppOptions{Listen: "127.0.0.1:0", Open: true, Output: writer})
	}()
	if _, err := bufio.NewReader(reader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var contents []byte
	for time.Now().Before(deadline) {
		contents, err = os.ReadFile(environmentFile)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	_ = writer.Close()
	_ = reader.Close()
	environment := string(contents)
	for _, expected := range []string{"DISPLAY=:42", "WAYLAND_DISPLAY=wayland-42", "DBUS_SESSION_BUS_ADDRESS=unix:path=/synthetic-bus", "XDG_CURRENT_DESKTOP=synthetic-desktop"} {
		if !strings.Contains(environment, expected) {
			t.Errorf("opener environment missing %q: %s", expected, environment)
		}
	}
	if strings.Contains(environment, "ASC_PRIVATE_KEY") || strings.Contains(environment, "synthetic-account-secret") {
		t.Fatalf("opener inherited account secret: %s", environment)
	}
}

func TestJSONEnvelopeShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeData(recorder, http.StatusOK, map[string]string{"value": "ok"})
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"ok":true`)) || bytes.Contains(recorder.Body.Bytes(), []byte(`"error"`)) {
		t.Fatalf("unexpected success envelope: %s", recorder.Body.Bytes())
	}
}

func TestAPIPlansWorkspaceSetupWithoutAProjectAndStrictlyValidatesFields(t *testing.T) {
	server, _, _ := testAPI(t)
	response := request(t, server, http.MethodPost, "/api/plan", `{"action":"tool-install","tool":"asc"}`, "test-token", "")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("workspace plan status=%d body=%s", response.StatusCode, body)
	}
	var envelope struct {
		Data pomeforge.Plan `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if envelope.Data.Scope != "workspace" || envelope.Data.ToolInstall == nil || envelope.Data.Steps[0].Kind != "internal" || envelope.Data.Steps[0].Executable != "" {
		t.Fatalf("unexpected workspace plan: %#v", envelope.Data)
	}
	for _, body := range []string{
		`{"action":"tool-install","tool":"asc","project":"Demo"}`,
		`{"action":"sdk-import","inputPath":"relative/Xcode.app","arch":"x86_64"}`,
		`{"action":"helper-register","helper":"unxip","executablePath":"/tmp/unxip","assetKitRevision":"e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7"}`,
		`{"action":"build","project":"Demo","tool":"asc"}`,
	} {
		response = request(t, server, http.MethodPost, "/api/plan", body, "test-token", "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid body %s status=%d", body, response.StatusCode)
		}
		_ = response.Body.Close()
	}
}

func TestAPIErrorEnvelopeIncludesCompletedResult(t *testing.T) {
	recorder := httptest.NewRecorder()
	result := pomeforge.OperationResult{ID: "result-1", Action: "sdk-import", Status: "failed", ExitCode: 9, Scope: "workspace", Output: "bounded failure"}
	writeError(recorder, http.StatusUnprocessableEntity, pomeforge.ErrorWithResult("operation_failed", "operation failed", result))
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"result":{"id":"result-1"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"scope":"workspace"`)) {
		t.Fatalf("result-bearing error envelope = %s", recorder.Body.Bytes())
	}
}
