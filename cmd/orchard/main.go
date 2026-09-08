package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"orchard.local/orchard/internal/orchard"
	webapp "orchard.local/orchard/internal/web"
)

var defaultTools orchard.ToolResolver

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type cli struct {
	json   bool
	stdout io.Writer
	stderr io.Writer
}

type responseEnvelope struct {
	OK    bool           `json:"ok"`
	Data  any            `json:"data,omitempty"`
	Error *responseError `json:"error,omitempty"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type schemaParameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

type schemaCommand struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  []schemaParameter `json:"parameters"`
	Effect      string            `json:"effect"`
}

func run(arguments []string, stdout, stderr io.Writer) int {
	filtered, jsonMode, err := extractJSONFlag(arguments)
	command := &cli{json: jsonMode, stdout: stdout, stderr: stderr}
	if err != nil {
		return command.fail(err, 2)
	}
	if len(filtered) == 1 && (filtered[0] == "--help" || filtered[0] == "-h" || filtered[0] == "help") {
		return command.success(map[string]string{"help": generalHelp()}, generalHelp())
	}
	if len(filtered) == 0 {
		return command.fail(orchard.Errorf("usage", "a command is required; run `orchard --help` or `orchard schema --json`"), 2)
	}
	name, args := filtered[0], filtered[1:]
	if isHelpRequest(name, args) {
		help, ok := commandHelp(name)
		if !ok {
			return command.fail(orchard.Errorf("unknown_command", "unknown command: "+name), 2)
		}
		return command.success(map[string]string{"help": help}, help)
	}
	switch name {
	case "version":
		if len(args) != 0 {
			return command.fail(orchard.Errorf("usage", "version accepts no arguments"), 2)
		}
		return command.success(map[string]string{"version": orchard.Version}, "Orchard "+orchard.Version)
	case "schema":
		if len(args) != 0 {
			return command.fail(orchard.Errorf("usage", "schema accepts no arguments"), 2)
		}
		return command.success(agentSchema(), "Use `orchard schema --json` for the machine-readable command and action schema.")
	case "init":
		return command.init(args)
	case "doctor":
		return command.doctor(args, false)
	case "tools":
		return command.doctor(args, true)
	case "plan":
		return command.operation(args, false)
	case "run":
		return command.operation(args, true)
	case "app":
		return command.app(args)
	default:
		return command.fail(orchard.Errorf("unknown_command", "unknown command: "+name), 2)
	}
}

func extractJSONFlag(arguments []string) ([]string, bool, error) {
	filtered := make([]string, 0, len(arguments))
	jsonMode := false
	for _, argument := range arguments {
		if argument == "--json" {
			jsonMode = true
			continue
		}
		if strings.HasPrefix(argument, "--json=") {
			return nil, false, orchard.Errorf("unknown_flag", "--json does not accept a value")
		}
		filtered = append(filtered, argument)
	}
	return filtered, jsonMode, nil
}

func isHelpRequest(command string, args []string) bool {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return true
	}
	if (command == "plan" || command == "run") && len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		_, known := actionByName(args[0])
		return known
	}
	return false
}

func actionByName(name string) (orchard.ActionInfo, bool) {
	for _, action := range orchard.Actions() {
		if action.ID == name {
			return action, true
		}
	}
	return orchard.ActionInfo{}, false
}

func generalHelp() string {
	return `Orchard — local Linux workspace for Swift iPhone and iPad development

Usage: orchard [--json] COMMAND [options]

Commands:
  version                 Print Orchard's version
  schema                  Print the machine-readable command/action schema
  init NAME               Create a project (--bundle-id is required)
  doctor                  Inspect tools and capabilities
  tools                   Inspect supported tool executables
  plan ACTION             Inspect an action without executing it
  run ACTION              Execute with --execute; signing/account effects need --confirm
  app                     Serve the authenticated loopback workspace

Run orchard COMMAND --help for command flags and effects.`
}

func commandHelp(name string) (string, bool) {
	help := map[string]string{
		"version": "Usage: orchard version [--json]\nPrint Orchard's version. Effect: none.",
		"schema":  "Usage: orchard schema [--json]\nPrint commands, flags, actions, effects, and confirmation requirements.",
		"init":    "Usage: orchard init NAME [--dir PATH] --bundle-id ID [--json]\nCreate a SwiftUI project without overwriting an existing path. Effect: filesystem write.",
		"doctor":  "Usage: orchard doctor [--json]\nRun bounded credential-free tool probes and report capability prerequisites. Effect: local read.",
		"tools":   "Usage: orchard tools [--json]\nRun bounded credential-free version probes. Effect: local read.",
		"plan":    "Usage: orchard plan ACTION --project PATH [--ipa PATH] [--device ID] [--json]\nActions: setup, build, devices, install, launch, export, store-status, validate, upload, submit.\nInspect exact argv, warnings, blockers, effects, and confirmation needs without executing.",
		"run":     "Usage: orchard run ACTION --project PATH --execute [--ipa PATH] [--device ID] [--confirm] [--json]\nActions: setup, build, devices, install, launch, export, store-status, validate, upload, submit.\nExecute a regenerated plan. --confirm is required for App Store writes and xtool install/dev-run signing effects.",
		"app":     "Usage: orchard app [--workspace PATH] [--listen 127.0.0.1:PORT] [--open] [--json]\nServe the token-authenticated loopback workspace; --open invokes xdg-open without account secrets.",
	}
	value, ok := help[name]
	return value, ok
}

func (c *cli) init(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return c.fail(orchard.Errorf("usage", "usage: orchard init NAME [--dir PATH] --bundle-id ID"), 2)
	}
	name, args := args[0], args[1:]
	flags := newFlagSet("init")
	directory := flags.String("dir", name, "destination directory")
	bundleID := flags.String("bundle-id", "", "bundle identifier")
	if err := flags.Parse(args); err != nil {
		return c.fail(flagError(err), 2)
	}
	if flags.NArg() != 0 {
		return c.fail(orchard.Errorf("usage", "unexpected argument: "+flags.Arg(0)), 2)
	}
	if *bundleID == "" {
		return c.fail(orchard.Errorf("usage", "--bundle-id is required"), 2)
	}
	workspace, relative, err := creationContext(*directory)
	if err != nil {
		return c.fail(err, 2)
	}
	service, err := orchard.NewService(workspace, defaultTools)
	if err != nil {
		return c.fail(err, 2)
	}
	project, err := service.CreateProject(relative, name, *bundleID)
	if err != nil {
		return c.fail(err, exitFor(err))
	}
	return c.success(project, fmt.Sprintf("Created %s at %s", project.Name, project.Path))
}

func creationContext(destination string) (string, string, error) {
	target, err := filepath.Abs(destination)
	if err != nil {
		return "", "", orchard.Errorf("invalid_path", err.Error())
	}
	ancestor := filepath.Dir(target)
	for {
		info, statErr := os.Stat(ancestor)
		if statErr == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", "", orchard.Errorf("invalid_path", "no existing parent directory for destination")
		}
		ancestor = parent
	}
	workspace, err := orchard.CanonicalWorkspace(ancestor)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(workspace, target)
	if err != nil {
		return "", "", orchard.Errorf("invalid_path", err.Error())
	}
	return workspace, relative, nil
}

func (c *cli) doctor(args []string, toolsOnly bool) int {
	flags := newFlagSet("doctor")
	if err := flags.Parse(args); err != nil {
		return c.fail(flagError(err), 2)
	}
	if flags.NArg() != 0 {
		return c.fail(orchard.Errorf("usage", "unexpected argument: "+flags.Arg(0)), 2)
	}
	workspace, err := orchard.CanonicalWorkspace(".")
	if err != nil {
		return c.fail(err, 2)
	}
	service, err := orchard.NewService(workspace, defaultTools)
	if err != nil {
		return c.fail(err, 2)
	}
	ctx := context.Background()
	if toolsOnly {
		tools := service.Tools.ProbeAll(ctx)
		return c.success(tools, humanTools(tools))
	}
	state, err := service.State(ctx)
	if err != nil {
		return c.fail(err, 1)
	}
	data := struct {
		Tools        []orchard.ToolStatus `json:"tools"`
		Capabilities []orchard.Capability `json:"capabilities"`
	}{state.Tools, state.Capabilities}
	return c.success(data, humanDoctor(data.Tools, data.Capabilities))
}

func (c *cli) operation(args []string, execute bool) int {
	verb := "plan"
	if execute {
		verb = "run"
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return c.fail(orchard.Errorf("usage", "usage: orchard "+verb+" ACTION --project PATH [--ipa PATH] [--device ID]"), 2)
	}
	action, args := args[0], args[1:]
	flags := newFlagSet(verb)
	project := flags.String("project", "", "project path")
	ipa := flags.String("ipa", "", "IPA path")
	device := flags.String("device", "", "device identifier")
	executeFlag := false
	confirm := false
	if execute {
		flags.BoolVar(&executeFlag, "execute", false, "execute the inspected operation")
		flags.BoolVar(&confirm, "confirm", false, "confirm an account write")
	}
	if err := flags.Parse(args); err != nil {
		return c.fail(flagError(err), 2)
	}
	if flags.NArg() != 0 {
		return c.fail(orchard.Errorf("usage", "unexpected argument: "+flags.Arg(0)), 2)
	}
	if *project == "" {
		return c.fail(orchard.Errorf("usage", "--project is required"), 2)
	}
	if execute && !executeFlag {
		return c.fail(orchard.Errorf("execute_required", "orchard run requires --execute"), 2)
	}
	service, projectValue, err := projectService(*project)
	if err != nil {
		return c.fail(err, exitFor(err))
	}
	input := orchard.PlanInput{Action: action, Project: projectValue, IPA: *ipa, Device: *device}
	if !execute {
		plan, planErr := service.PlanOperation(context.Background(), input, false)
		if planErr != nil {
			return c.fail(planErr, exitFor(planErr))
		}
		return c.success(plan, humanPlan(plan))
	}
	result, runErr := service.RunImmediate(context.Background(), input, confirm)
	if runErr != nil {
		return c.fail(runErr, exitFor(runErr))
	}
	exitCode := result.ExitCode
	if exitCode < 0 {
		exitCode = 1
	}
	if c.json {
		_ = json.NewEncoder(c.stdout).Encode(responseEnvelope{OK: true, Data: result})
	} else {
		fmt.Fprintf(c.stdout, "%s: %s (exit %d)\n%s", result.Action, result.Status, result.ExitCode, result.Output)
	}
	return exitCode
}

func projectService(requested string) (*orchard.Service, string, error) {
	abs, err := filepath.Abs(requested)
	if err != nil {
		return nil, "", orchard.Errorf("invalid_path", err.Error())
	}
	cwd, err := orchard.CanonicalWorkspace(".")
	if err != nil {
		return nil, "", err
	}
	workspace := cwd
	relative, relErr := filepath.Rel(cwd, abs)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		workspace, err = orchard.CanonicalWorkspace(filepath.Dir(abs))
		if err != nil {
			return nil, "", err
		}
		relative = filepath.Base(abs)
	}
	service, err := orchard.NewService(workspace, defaultTools)
	return service, relative, err
}

func (c *cli) app(args []string) int {
	flags := newFlagSet("app")
	workspace := flags.String("workspace", ".", "workspace path")
	listen := flags.String("listen", "127.0.0.1:8787", "loopback listen address")
	openBrowser := flags.Bool("open", false, "open the desktop browser")
	if err := flags.Parse(args); err != nil {
		return c.fail(flagError(err), 2)
	}
	if flags.NArg() != 0 {
		return c.fail(orchard.Errorf("usage", "unexpected argument: "+flags.Arg(0)), 2)
	}
	service, err := orchard.NewService(*workspace, defaultTools)
	if err != nil {
		return c.fail(err, exitFor(err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := webapp.ServeApp(ctx, service, webapp.AppOptions{Listen: *listen, Open: *openBrowser, JSON: c.json, Output: c.stdout, Warnings: c.stderr}); err != nil {
		return c.fail(err, exitFor(err))
	}
	return 0
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func flagError(err error) error {
	message := err.Error()
	if strings.Contains(message, "flag provided but not defined") {
		return orchard.Errorf("unknown_flag", message)
	}
	return orchard.Errorf("usage", message)
}

func (c *cli) success(data any, human string) int {
	if c.json {
		_ = json.NewEncoder(c.stdout).Encode(responseEnvelope{OK: true, Data: data})
	} else {
		fmt.Fprintln(c.stdout, human)
	}
	return 0
}

func (c *cli) fail(err error, code int) int {
	errCode, message := "error", err.Error()
	var coded *orchard.CodedError
	if errors.As(err, &coded) {
		errCode, message = coded.Code, coded.Message
	}
	if c.json {
		_ = json.NewEncoder(c.stdout).Encode(responseEnvelope{OK: false, Error: &responseError{Code: errCode, Message: message}})
	} else {
		fmt.Fprintln(c.stderr, "Error:", message)
	}
	return code
}

func exitFor(err error) int {
	var coded *orchard.CodedError
	if !errors.As(err, &coded) {
		return 1
	}
	switch coded.Code {
	case "blocked", "confirmation_required":
		return 3
	case "stale_plan", "operation_in_progress", "already_exists":
		return 4
	default:
		return 2
	}
}

func humanTools(tools []orchard.ToolStatus) string {
	var builder strings.Builder
	for _, tool := range tools {
		fmt.Fprintf(&builder, "%-16s %-12s %s\n", tool.Name, tool.Status, tool.Detail)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func humanDoctor(tools []orchard.ToolStatus, capabilities []orchard.Capability) string {
	var builder strings.Builder
	builder.WriteString("Tools\n")
	builder.WriteString(humanTools(tools))
	builder.WriteString("\n\nCapabilities\n")
	for _, capability := range capabilities {
		fmt.Fprintf(&builder, "%-24s %-10s %s\n", capability.Title, capability.Status, capability.Detail)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func humanPlan(plan orchard.Plan) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s (%s)\nPlan: %s\nEffect: %s\nExecutable: %t\n", plan.Title, plan.Action, plan.ID, plan.Effect, plan.Executable)
	for index, step := range plan.Steps {
		fmt.Fprintf(&builder, "%d. %s", index+1, step.Executable)
		for _, argument := range step.Args {
			builder.WriteByte(' ')
			builder.WriteString(strconv.Quote(argument))
		}
		fmt.Fprintf(&builder, "\n   %s\n", step.Description)
	}
	for _, blocker := range plan.Blockers {
		fmt.Fprintln(&builder, "Blocked:", blocker)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintln(&builder, "Warning:", warning)
	}
	return strings.TrimRight(builder.String(), "\n")
}

func agentSchema() any {
	commands := []schemaCommand{
		{Name: "version", Description: "Print Orchard's version.", Parameters: []schemaParameter{}, Effect: "none"},
		{Name: "schema", Description: "Discover commands, actions, parameters, and effects.", Parameters: []schemaParameter{}, Effect: "none"},
		{Name: "init", Description: "Create a SwiftUI iPhone and iPad project without overwriting.", Parameters: []schemaParameter{{"name", "string", true, "Simple Swift project name."}, {"--dir", "path", false, "Destination directory; defaults to NAME."}, {"--bundle-id", "string", true, "Reverse-DNS bundle identifier."}}, Effect: "filesystem-write"},
		{Name: "doctor", Description: "Probe prerequisites and capabilities.", Parameters: []schemaParameter{}, Effect: "local-read"},
		{Name: "tools", Description: "Probe supported tool executables and versions.", Parameters: []schemaParameter{}, Effect: "local-read"},
		{Name: "plan", Description: "Inspect a deterministic action plan without executing it.", Parameters: operationParameters(false), Effect: "local-read"},
		{Name: "run", Description: "Regenerate and explicitly execute an action plan.", Parameters: operationParameters(true), Effect: "action-dependent"},
		{Name: "app", Description: "Serve the authenticated loopback workspace.", Parameters: []schemaParameter{{"--workspace", "path", false, "Workspace root."}, {"--listen", "address", false, "127.0.0.1 host and port."}, {"--open", "boolean", false, "Open with xdg-open."}}, Effect: "local-server"},
	}
	return struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Global        []schemaParameter    `json:"globalParameters"`
		Commands      []schemaCommand      `json:"commands"`
		Actions       []orchard.ActionInfo `json:"actions"`
	}{1, []schemaParameter{{"--json", "boolean", false, "Emit a JSON envelope; accepted before or after the command."}}, commands, orchard.Actions()}
}

func operationParameters(execute bool) []schemaParameter {
	parameters := []schemaParameter{
		{"action", "enum", true, "One of the discoverable action IDs."},
		{"--project", "path", true, "Orchard project directory."},
		{"--ipa", "path", false, "Workspace-confined IPA for install or upload."},
		{"--device", "string", false, "Physical device UDID."},
	}
	if execute {
		parameters = append(parameters, schemaParameter{"--execute", "boolean", true, "Required execution gate."}, schemaParameter{"--confirm", "boolean", false, "Required for account writes."})
	}
	return parameters
}
