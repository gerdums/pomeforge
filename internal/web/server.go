package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"orchard.local/orchard/internal/orchard"
)

// The app packet supplies index.html, app.js, and style.css. all: keeps this
// package buildable with the explicitly permitted placeholder in the meantime.
//
//go:embed all:assets/*
var assetFiles embed.FS

const maxRequestBody = 1 << 20

type API struct {
	Service       *orchard.Service
	Token         string
	AllowedOrigin string
}

type envelope struct {
	OK    bool       `json:"ok"`
	Data  any        `json:"data,omitempty"`
	Error *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("POST /api/projects", a.projects)
	mux.HandleFunc("POST /api/plan", a.plan)
	mux.HandleFunc("POST /api/run", a.run)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, orchard.Errorf("not_found", "API endpoint not found"))
	})
	mux.HandleFunc("/", a.assets)
	return a.securityHeaders(a.validateRequest(mux))
}

func (a *API) validateRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) {
			writeError(w, http.StatusForbidden, orchard.Errorf("invalid_host", "foreign Host header rejected"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if a.AllowedOrigin == "" || origin != a.AllowedOrigin {
				writeError(w, http.StatusForbidden, orchard.Errorf("invalid_origin", "foreign Origin header rejected"))
				return
			}
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if provided == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(provided), []byte(a.Token)) != 1 {
				writeError(w, http.StatusUnauthorized, orchard.Errorf("unauthorized", "a valid Bearer token is required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func allowedHost(value string) bool {
	host := value
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (a *API) state(w http.ResponseWriter, r *http.Request) {
	state, err := a.Service.State(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeData(w, http.StatusOK, state)
}

func (a *API) projects(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name      string `json:"name"`
		BundleID  string `json:"bundleId"`
		Directory string `json:"directory"`
	}
	if err := decodePOST(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Directory == "" || filepath.IsAbs(request.Directory) {
		writeError(w, http.StatusBadRequest, orchard.Errorf("invalid_path", "API project directory must be relative to the workspace"))
		return
	}
	project, err := a.Service.CreateProject(request.Directory, request.Name, request.BundleID)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	relative, _ := pathRelative(a.Service.Workspace, project.Path)
	project.Path = relative
	writeData(w, http.StatusCreated, project)
}

func (a *API) plan(w http.ResponseWriter, r *http.Request) {
	var input orchard.PlanInput
	if err := decodePOST(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Project == "" || filepath.IsAbs(input.Project) || (input.IPA != "" && filepath.IsAbs(input.IPA)) {
		writeError(w, http.StatusBadRequest, orchard.Errorf("invalid_path", "API project and IPA paths must be relative to the workspace"))
		return
	}
	plan, err := a.Service.PlanOperation(r.Context(), input, true)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeData(w, http.StatusOK, plan)
}

func (a *API) run(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlanID  string `json:"planId"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodePOST(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := a.Service.RunStored(r.Context(), request.PlanID, request.Confirm)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func decodePOST(w http.ResponseWriter, r *http.Request, value any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return orchard.Errorf("invalid_content_type", "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return orchard.Errorf("invalid_json", "invalid JSON body: "+err.Error())
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return orchard.Errorf("invalid_json", "body must contain exactly one JSON value")
		}
		return orchard.Errorf("invalid_json", "invalid JSON body: "+err.Error())
	}
	return nil
}

func (a *API) assets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, orchard.Errorf("method_not_allowed", "method not allowed"))
		return
	}
	requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if requested == "" || requested == "." {
		requested = "index.html"
	}
	if strings.Contains(requested, "..") {
		http.NotFound(w, r)
		return
	}
	contents, err := fs.ReadFile(assetFiles, "assets/"+requested)
	if err != nil && requested == "index.html" {
		contents = []byte("<!doctype html><html><head><meta charset=utf-8><title>Orchard</title></head><body><main><h1>Orchard</h1><p>The workspace assets will be supplied by the app integration packet.</p></main></body></html>")
		err = nil
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(requested)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(contents)
	}
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: data})
}

func writeError(w http.ResponseWriter, status int, err error) {
	code := "internal_error"
	message := "internal error"
	var coded *orchard.CodedError
	if errors.As(err, &coded) {
		code, message = coded.Code, coded.Message
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{OK: false, Error: &errorBody{Code: code, Message: message}})
}

func statusFor(err error) int {
	var coded *orchard.CodedError
	if !errors.As(err, &coded) {
		return http.StatusInternalServerError
	}
	switch coded.Code {
	case "unauthorized":
		return http.StatusUnauthorized
	case "already_exists", "stale_plan", "operation_in_progress":
		return http.StatusConflict
	case "blocked", "confirmation_required":
		return http.StatusUnprocessableEntity
	case "plan_not_found", "manifest_not_found", "path_not_found":
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func pathRelative(root, target string) (string, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(relative, "\\", "/"), nil
}

type AppOptions struct {
	Listen   string
	Open     bool
	JSON     bool
	Output   io.Writer
	Warnings io.Writer
}

func ServeApp(ctx context.Context, service *orchard.Service, options AppOptions) error {
	address := options.Listen
	if address == "" {
		address = "127.0.0.1:8787"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return orchard.Errorf("invalid_listen", "--listen must be a 127.0.0.1 host:port address")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	token, err := sessionToken()
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	appURL := origin + "/#token=" + token
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	warnings := options.Warnings
	if warnings == nil {
		warnings = output
	}
	if options.JSON {
		_ = json.NewEncoder(output).Encode(envelope{OK: true, Data: map[string]string{"url": appURL}})
	} else {
		fmt.Fprintf(output, "Orchard app: %s\n", appURL)
	}
	if options.Open {
		path, lookupErr := exec.LookPath("xdg-open")
		if lookupErr != nil {
			fmt.Fprintln(warnings, "Warning: xdg-open is unavailable; open the Orchard app URL manually.")
		} else {
			command := exec.Command(path, appURL)
			command.Env = orchard.ChildEnvironment()
			if startErr := command.Start(); startErr != nil {
				fmt.Fprintln(warnings, "Warning: the browser opener could not start; open the Orchard app URL manually.")
			} else {
				go func() {
					if command.Wait() != nil {
						fmt.Fprintln(warnings, "Warning: the browser opener exited unsuccessfully; open the Orchard app URL manually.")
					}
				}()
			}
		}
	}
	server := &http.Server{
		Handler:           (&API{Service: service, Token: token, AllowedOrigin: origin}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      35 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdownCtx)
			cancel()
		case <-shutdown:
		}
	}()
	err = server.Serve(listener)
	close(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func sessionToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
