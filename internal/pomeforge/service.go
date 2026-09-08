package pomeforge

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Service struct {
	Workspace string
	Tools     ToolResolver
	Planner   Planner
	Setup     *SetupManager
	Release   *ReleaseManager
	Executor  *Executor

	mu    sync.Mutex
	plans map[string]PlanInput
}

func NewService(workspace string, tools ToolResolver) (*Service, error) {
	canonical, err := CanonicalWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	if tools == nil {
		integrated, resolverErr := DefaultIntegratedToolResolver()
		if resolverErr != nil {
			return nil, resolverErr
		}
		tools = integrated
	}
	setup, err := NewSetupManager(tools)
	if err != nil {
		return nil, err
	}
	service := &Service{Workspace: canonical, Tools: tools, Setup: setup, Executor: &Executor{Workspace: canonical, XDGConfigHome: setup.XDGConfigHome}, plans: make(map[string]PlanInput)}
	service.Planner = Planner{Workspace: canonical, Tools: tools, XDGConfigHome: setup.XDGConfigHome}
	service.Release = NewReleaseManager(canonical, setup.StateDir, tools, service.Executor)
	return service, nil
}

func (s *Service) CreateProject(directory, name, bundleID string) (ProjectSummary, error) {
	return CreateProject(s.Workspace, directory, name, bundleID)
}

func (s *Service) PlanOperation(ctx context.Context, input PlanInput, store bool) (Plan, error) {
	if isWorkspaceAction(input.Action) && input.reservation == "" {
		input.reservation = s.Workspace + "\x00" + operationID()
	}
	plan, err := s.plan(ctx, input)
	if err != nil {
		return Plan{}, err
	}
	if store {
		s.mu.Lock()
		s.plans[plan.ID] = input
		s.mu.Unlock()
	}
	return plan, nil
}

func (s *Service) RunStored(ctx context.Context, planID string, confirm bool) (OperationResult, error) {
	s.mu.Lock()
	input, ok := s.plans[planID]
	if ok {
		// A stored plan authorizes one attempt. Reserving it under the same lock
		// prevents both concurrent and later replay; an explicit new planning
		// request can store a fresh authorization opportunity.
		delete(s.plans, planID)
	}
	s.mu.Unlock()
	if !ok {
		return OperationResult{}, Errorf("plan_not_found", "unknown or expired plan ID")
	}
	plan, err := s.plan(ctx, input)
	if err != nil {
		return OperationResult{}, err
	}
	if plan.ID != planID {
		return OperationResult{}, Errorf("stale_plan", "project, operation inputs, manifest, source, configuration, or tool state changed; inspect a new plan")
	}
	if isReleaseAction(input.Action) {
		return s.Release.Execute(ctx, plan, input, confirm)
	}
	if isWorkspaceAction(input.Action) {
		return s.Setup.Execute(ctx, plan, input)
	}
	project, err := ResolveWithin(s.Workspace, input.Project, true)
	if err != nil {
		return OperationResult{}, err
	}
	return s.Executor.Execute(ctx, plan, project, confirm)
}

func (s *Service) RunImmediate(ctx context.Context, input PlanInput, confirm bool) (OperationResult, error) {
	if isWorkspaceAction(input.Action) && input.reservation == "" {
		input.reservation = s.Workspace + "\x00" + operationID()
	}
	plan, err := s.plan(ctx, input)
	if err != nil {
		return OperationResult{}, err
	}
	if isReleaseAction(input.Action) {
		return s.Release.Execute(ctx, plan, input, confirm)
	}
	if isWorkspaceAction(input.Action) {
		return s.Setup.Execute(ctx, plan, input)
	}
	project, err := ResolveWithin(s.Workspace, input.Project, true)
	if err != nil {
		return OperationResult{}, err
	}
	return s.Executor.Execute(ctx, plan, project, confirm)
}

func (s *Service) plan(ctx context.Context, input PlanInput) (Plan, error) {
	if isReleaseAction(input.Action) {
		if s.Release == nil {
			return Plan{}, Errorf("blocked", "release integration is not configured")
		}
		return s.Release.Plan(ctx, input)
	}
	if isWorkspaceAction(input.Action) {
		if s.Setup == nil {
			return Plan{}, Errorf("blocked", "workspace setup is not configured")
		}
		return s.Setup.Plan(ctx, input)
	}
	return s.Planner.Plan(ctx, input)
}

func (s *Service) State(ctx context.Context) (State, error) {
	projects, err := DiscoverProjects(s.Workspace)
	if err != nil {
		return State{}, err
	}
	tools := s.Tools.ProbeAll(ctx)
	if s.Setup != nil {
		tools = append(tools, s.Setup.ProbeSDK(ctx))
	}
	toolMap := make(map[string]ToolStatus, len(tools))
	for _, tool := range tools {
		toolMap[tool.ID] = tool
	}
	capabilities := []Capability{
		{ID: "project-generation", Title: "Project generation", Status: "available", Detail: "SwiftUI iPhone and iPad templates are generated locally."},
		{ID: "linux-runtime", Title: "Linux runtime prerequisites", Status: "unverified", Detail: "Successful executable probes do not replace distro checks for Swift runtime libraries, glibc/musl compatibility, or optional AppImage/FUSE requirements."},
		capabilityFromTools("local-build", "Local iOS build", toolMap, []string{"swift", "xtool", "darwin-sdk"}, "unverified", "Required tools and an installed Darwin SDK are available; a real project build is still required."),
		capabilityFromTools("asset-catalogs", "Asset catalog compiler", toolMap, []string{"swift", "pomeforge-assets"}, "unverified", "The registered bridge is available; an actual catalog compile is still required."),
		capabilityFromTools("xip-extraction", "Linux XIP extraction", toolMap, []string{"unxip"}, "unverified", "The registered unxip helper is available; extraction does not authenticate Apple's XIP signature."),
		capabilityFromTools("physical-device", "Physical device", toolMap, []string{"xtool", "usbmuxd"}, "unverified", "Tool executables are available; a trusted device, Developer Mode, and compatible signing are still required."),
		capabilityFromTools("app-store-connect", "App Store Connect", toolMap, []string{"asc"}, "unverified", "ASC is available; private account authentication and project resource IDs are validated at execution time."),
		capabilityFromTools("distribution-signing", "Distribution signing", toolMap, []string{"zsign"}, "unverified", "Native export is available; a valid named App Store identity, active SDK provenance, and real candidate export are still required."),
	}
	history := make([]OperationResult, 0)
	if s.Setup != nil {
		if historyRoot, historyRootErr := s.Setup.workspaceHistoryRootFor(s.Workspace); historyRootErr == nil {
			if items, historyErr := loadHistoryWithin(s.Setup.StateDir, historyRoot, 40); historyErr == nil {
				history = append(history, items...)
			}
		}
	}
	for _, project := range projects {
		path := filepath.Join(s.Workspace, project.Path)
		items, historyErr := loadHistoryWithin(s.Workspace, path, 20)
		if historyErr == nil {
			history = append(history, items...)
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].StartedAt.After(history[j].StartedAt) })
	if len(history) > 100 {
		history = history[:100]
	}
	identities := []SigningIdentity{}
	if s.Release != nil {
		identities = s.Release.ListIdentities(ctx)
	}
	return State{Version: Version, Workspace: s.Workspace, Projects: projects, Tools: tools, Capabilities: capabilities, Actions: Actions(), History: history, Identities: identities}, nil
}

func capabilityFromTools(id, title string, tools map[string]ToolStatus, required []string, readyStatus, detail string) Capability {
	missing := make([]string, 0)
	for _, toolID := range required {
		tool, present := tools[toolID]
		if !present || tool.Status != "available" {
			name := tool.Name
			if name == "" {
				name = toolID
			}
			status := tool.Status
			if status == "" {
				status = "missing"
			}
			reason := tool.Detail
			if reason == "" {
				reason = "no verified diagnostic was reported"
			}
			missing = append(missing, name+" is "+status+" ("+reason+")")
		}
	}
	if len(missing) > 0 {
		return Capability{ID: id, Title: title, Status: "blocked", Detail: "Required prerequisites are unavailable: " + strings.Join(missing, "; ") + ". Install or verify them, then refresh diagnostics."}
	}
	return Capability{ID: id, Title: title, Status: readyStatus, Detail: detail}
}
