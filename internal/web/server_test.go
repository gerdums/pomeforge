package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orchard.local/orchard/internal/orchard"
)

type webTools map[string]orchard.ToolStatus

func (f webTools) Probe(_ context.Context, id string) orchard.ToolStatus {
	if status, ok := f[id]; ok {
		return status
	}
	return orchard.ToolStatus{ID: id, Name: id, Status: "missing", Detail: "missing", InstallURL: "https://example.invalid"}
}

func (f webTools) ProbeAll(ctx context.Context) []orchard.ToolStatus {
	result := make([]orchard.ToolStatus, 0)
	for _, id := range []string{"xtool", "swift", "asc", "zsign", "idevice_id", "usbmuxd"} {
		result = append(result, f.Probe(ctx, id))
	}
	return result
}

func testAPI(t *testing.T) (*httptest.Server, *API, string) {
	t.Helper()
	workspace := t.TempDir()
	tools := webTools{}
	for _, id := range []string{"xtool", "swift", "asc"} {
		tools[id] = orchard.ToolStatus{ID: id, Name: id, Status: "available", Version: "test", Path: "/test/" + id, Detail: "test"}
	}
	service, err := orchard.NewService(workspace, tools)
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
	manifest, _, err := orchard.LoadManifest(filepath.Join(workspace, "Demo"))
	if err != nil {
		t.Fatal(err)
	}
	manifest.AppStore.AppID = "1001"
	encoded, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(workspace, "Demo", "orchard.json"), append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "Demo.ipa"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	response = request(t, server, http.MethodPost, "/api/plan", `{"action":"upload","project":"Demo","ipa":"Demo.ipa"}`, "test-token", "")
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("plan status = %d: %s", response.StatusCode, body)
	}
	var wrapped struct {
		OK   bool         `json:"ok"`
		Data orchard.Plan `json:"data"`
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

func TestAPIRejectsStaleStoredPlan(t *testing.T) {
	server, _, workspace := testAPI(t)
	project, err := orchard.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server, http.MethodPost, "/api/plan", `{"action":"build","project":"Demo"}`, "test-token", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d", response.StatusCode)
	}
	var wrapped struct {
		Data orchard.Plan `json:"data"`
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

func TestServeAppPrintsActualTokenURLAndRejectsForeignBind(t *testing.T) {
	service, err := orchard.NewService(t.TempDir(), webTools{})
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
	if !strings.HasPrefix(line, "Orchard app: http://127.0.0.1:") || !strings.Contains(line, "/#token=") {
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

func TestJSONEnvelopeShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeData(recorder, http.StatusOK, map[string]string{"value": "ok"})
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"ok":true`)) || bytes.Contains(recorder.Body.Bytes(), []byte(`"error"`)) {
		t.Fatalf("unexpected success envelope: %s", recorder.Body.Bytes())
	}
}
