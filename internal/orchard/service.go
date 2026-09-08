package orchard

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
)

type Service struct {
	Workspace string
	Tools     ToolResolver
	Planner   Planner
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
		tools = SystemToolResolver{}
	}
	service := &Service{Workspace: canonical, Tools: tools, Executor: &Executor{}, plans: make(map[string]PlanInput)}
	service.Planner = Planner{Workspace: canonical, Tools: tools}
	return service, nil
}

func (s *Service) CreateProject(directory, name, bundleID string) (ProjectSummary, error) {
	return CreateProject(s.Workspace, directory, name, bundleID)
}

func (s *Service) PlanOperation(ctx context.Context, input PlanInput, store bool) (Plan, error) {
	plan, err := s.Planner.Plan(ctx, input)
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
	plan, err := s.Planner.Plan(ctx, input)
	if err != nil {
		return OperationResult{}, err
	}
	if plan.ID != planID {
		return OperationResult{}, Errorf("stale_plan", "project, operation inputs, manifest, source, configuration, or tool state changed; inspect a new plan")
	}
	project, err := ResolveWithin(s.Workspace, input.Project, true)
	if err != nil {
		return OperationResult{}, err
	}
	return s.Executor.Execute(ctx, plan, project, confirm)
}

func (s *Service) RunImmediate(ctx context.Context, input PlanInput, confirm bool) (OperationResult, error) {
	plan, err := s.Planner.Plan(ctx, input)
	if err != nil {
		return OperationResult{}, err
	}
	project, err := ResolveWithin(s.Workspace, input.Project, true)
	if err != nil {
		return OperationResult{}, err
	}
	return s.Executor.Execute(ctx, plan, project, confirm)
}

func (s *Service) State(ctx context.Context) (State, error) {
	projects, err := DiscoverProjects(s.Workspace)
	if err != nil {
		return State{}, err
	}
	tools := s.Tools.ProbeAll(ctx)
	toolMap := make(map[string]ToolStatus, len(tools))
	for _, tool := range tools {
		toolMap[tool.ID] = tool
	}
	capabilities := []Capability{
		{ID: "project-generation", Title: "Project generation", Status: "available", Detail: "SwiftUI iPhone and iPad templates are generated locally."},
		capabilityFromTools("local-build", "Local iOS build", toolMap, []string{"swift", "xtool"}, "unverified", "Tool executables are available; a user-installed Darwin SDK and a real build are still required."),
		capabilityFromTools("physical-device", "Physical device", toolMap, []string{"xtool", "usbmuxd"}, "unverified", "Tool executables are available; a trusted device, Developer Mode, and compatible signing are still required."),
		capabilityFromTools("app-store-connect", "App Store Connect", toolMap, []string{"asc"}, "unverified", "ASC is available; private account authentication and project resource IDs are validated at execution time."),
		{ID: "distribution-signing", Title: "Distribution signing", Status: "blocked", Detail: "Distribution signing and signing validation are not implemented; development signing is not a substitute."},
	}
	history := make([]OperationResult, 0)
	for _, project := range projects {
		path := filepath.Join(s.Workspace, project.Path)
		items, historyErr := LoadHistory(path, 20)
		if historyErr == nil {
			history = append(history, items...)
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].StartedAt.After(history[j].StartedAt) })
	if len(history) > 100 {
		history = history[:100]
	}
	return State{Version: Version, Workspace: s.Workspace, Projects: projects, Tools: tools, Capabilities: capabilities, Actions: Actions(), History: history}, nil
}

func capabilityFromTools(id, title string, tools map[string]ToolStatus, required []string, readyStatus, detail string) Capability {
	status := readyStatus
	for _, toolID := range required {
		if tools[toolID].Status != "available" {
			status = "blocked"
			break
		}
	}
	return Capability{ID: id, Title: title, Status: status, Detail: detail}
}
