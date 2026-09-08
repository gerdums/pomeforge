(function (root) {
  "use strict";

  const IPA_ACTIONS = new Set(["install", "validate", "upload", "submit"]);
  const DEVICE_ACTIONS = new Set(["install", "launch"]);

  function readSessionToken(locationObject, historyObject) {
    const fragment = String(locationObject && locationObject.hash ? locationObject.hash : "");
    let token = "";

    if (fragment.length > 1) {
      const parameters = new URLSearchParams(fragment.slice(1));
      token = parameters.get("token") || "";
      const cleanLocation = `${locationObject.pathname || "/"}${locationObject.search || ""}`;
      historyObject.replaceState(historyObject.state || null, "", cleanLocation);
    }

    return token;
  }

  function safeHttpsUrl(value) {
    if (typeof value !== "string" || value.length === 0) return "";
    try {
      const parsed = new URL(value);
      return parsed.protocol === "https:" ? parsed.href : "";
    } catch (_error) {
      return "";
    }
  }

  function createApp(options) {
    const doc = options.document;
    const fetcher = options.fetch;
    const token = options.token;
    const elements = {};
    const ids = [
      "connection-status", "project-select", "project-path", "app-version", "workspace-path",
      "app-alert", "alert-title", "alert-message", "retry-connection", "work-view",
      "prerequisites-view", "results-view", "work-title", "project-summary", "empty-projects",
      "work-content", "action-select", "action-description", "ipa-field", "ipa-input",
      "device-field", "device-input", "review-plan", "plan-status", "plan-panel", "plan-summary",
      "plan-blockers", "blocker-list", "plan-warnings", "warning-list", "plan-steps",
      "confirmation-field", "confirm-execution", "run-plan", "execution-status", "refresh-diagnostics",
      "tool-count", "tool-list", "capability-list", "result-list", "create-project-dialog",
      "create-project-form", "project-name", "bundle-id", "project-directory", "create-status",
      "submit-create-project", "close-create-project", "cancel-create-project"
    ];

    ids.forEach((id) => { elements[id] = doc.getElementById(id); });

    const state = {
      server: null,
      selectedProject: "",
      selectedAction: "",
      currentPlan: null,
      inputRevision: 0,
      planRequestSequence: 0,
      disconnected: false,
      busy: { state: false, plan: false, run: false, create: false },
      previousFocus: null,
      results: []
    };

    function createElement(tag, className, textValue) {
      const node = doc.createElement(tag);
      if (className) node.className = className;
      if (textValue !== undefined && textValue !== null) node.textContent = String(textValue);
      return node;
    }

    function normalizedList(value) {
      return Array.isArray(value) ? value : [];
    }

    function textValue(value, fallback) {
      return typeof value === "string" && value.length > 0 ? value : fallback;
    }

    function setConnection(label, status) {
      elements["connection-status"].textContent = label;
      elements["connection-status"].classList.toggle("is-online", status === "online");
      elements["connection-status"].classList.toggle("is-offline", status === "offline");
    }

    function showAlert(title, message, retryable) {
      elements["alert-title"].textContent = title;
      elements["alert-message"].textContent = message;
      elements["retry-connection"].hidden = !retryable;
      elements["app-alert"].hidden = false;
    }

    function clearAlert() {
      elements["app-alert"].hidden = true;
      elements["alert-title"].textContent = "";
      elements["alert-message"].textContent = "";
    }

    function connectionFailure(message) {
      state.disconnected = true;
      setConnection("Connection unavailable", "offline");
      showAlert(
        "Orchard connection unavailable",
        `${message} Retry this connection, or close this tab and reopen Orchard from the desktop launcher or with “orchard app --open”.`,
        Boolean(token)
      );
      updateControls();
    }

    async function apiRequest(path, init) {
      if (state.disconnected) throw new Error("Connection is disabled until retry.");
      if (!token) {
        connectionFailure("This page does not have a session token.");
        throw new Error("Missing session token.");
      }

      const request = init || {};
      const headers = { Accept: "application/json", Authorization: `Bearer ${token}` };
      if (request.body !== undefined) headers["Content-Type"] = "application/json";

      let response;
      try {
        response = await fetcher(path, {
          method: request.method || "GET",
          headers,
          body: request.body === undefined ? undefined : JSON.stringify(request.body)
        });
      } catch (_error) {
        connectionFailure("The local Orchard process did not respond.");
        throw new Error("The local Orchard process did not respond.");
      }

      let payload;
      try {
        payload = await response.json();
      } catch (_error) {
        showAlert("Unexpected response", "Orchard returned a response that was not valid JSON.", false);
        throw new Error("Orchard returned invalid JSON.");
      }

      if (!response.ok || !payload || payload.ok !== true) {
        const apiMessage = payload && payload.error && typeof payload.error.message === "string"
          ? payload.error.message
          : `Orchard returned HTTP ${response.status}.`;
        if (response.status === 401 || response.status === 403) {
          connectionFailure(`${apiMessage} The session may have expired.`);
        } else {
          showAlert("Request failed", apiMessage, false);
        }
        throw new Error(apiMessage);
      }

      return payload.data;
    }

    function anyBusy() {
      return Object.values(state.busy).some(Boolean);
    }

    function updateControls() {
      const unavailable = state.disconnected || anyBusy();
      const hasProjects = Boolean(state.server && normalizedList(state.server.projects).length);
      const hasActions = Boolean(state.server && normalizedList(state.server.actions).length);
      const plan = state.currentPlan;
      const blockers = plan ? normalizedList(plan.blockers) : [];
      const confirmationSatisfied = !plan || !plan.requiresConfirmation || elements["confirm-execution"].checked;

      elements["project-select"].disabled = unavailable || !hasProjects;
      elements["action-select"].disabled = unavailable || !hasActions || !state.selectedProject;
      elements["ipa-input"].disabled = unavailable;
      elements["device-input"].disabled = unavailable;
      elements["review-plan"].disabled = unavailable || !state.selectedProject || !state.selectedAction;
      elements["run-plan"].disabled = unavailable || !plan || plan.executable !== true || blockers.length > 0 || !confirmationSatisfied;
      elements["refresh-diagnostics"].disabled = unavailable;
      elements["submit-create-project"].disabled = unavailable;

      const createButtons = doc.querySelectorAll("#open-create-project, #header-create-project, #empty-create-project");
      createButtons.forEach((button) => { button.disabled = unavailable; });
    }

    function invalidatePlan(message) {
      state.inputRevision += 1;
      state.planRequestSequence += 1;
      state.currentPlan = null;
      elements["confirm-execution"].checked = false;
      elements["plan-panel"].hidden = true;
      elements["execution-status"].textContent = "";
      elements["plan-status"].textContent = message || "Inputs changed. Review a new plan before running.";
      updateControls();
    }

    function selectedProjectRecord() {
      return normalizedList(state.server && state.server.projects).find((project) => project.path === state.selectedProject) || null;
    }

    function selectedActionRecord() {
      return normalizedList(state.server && state.server.actions).find((action) => action.id === state.selectedAction) || null;
    }

    function renderProjectDetails() {
      const project = selectedProjectRecord();
      if (!project) {
        elements["work-title"].textContent = "Choose a project";
        elements["project-summary"].textContent = "Select an existing project or create one to inspect an operation.";
        elements["project-path"].textContent = state.server ? textValue(state.server.workspace, "Workspace has no projects") : "Waiting for workspace";
        return;
      }

      elements["work-title"].textContent = textValue(project.name, project.path);
      elements["project-summary"].textContent = project.bundleId
        ? `Bundle identifier ${project.bundleId}`
        : "No bundle identifier was reported.";
      elements["project-path"].textContent = project.path;
    }

    function renderProjects(preferredProject) {
      const projects = normalizedList(state.server.projects);
      const previousSelection = preferredProject || state.selectedProject;
      elements["project-select"].replaceChildren();

      if (projects.length === 0) {
        const option = createElement("option", "", "No projects");
        option.value = "";
        elements["project-select"].append(option);
        state.selectedProject = "";
      } else {
        projects.forEach((project) => {
          const option = createElement("option", "", textValue(project.name, project.path));
          option.value = project.path;
          elements["project-select"].append(option);
        });
        state.selectedProject = projects.some((project) => project.path === previousSelection)
          ? previousSelection
          : projects[0].path;
      }

      elements["project-select"].value = state.selectedProject;
      elements["empty-projects"].hidden = projects.length !== 0;
      elements["work-content"].hidden = projects.length === 0;
      renderProjectDetails();
    }

    function renderActionFields() {
      const action = selectedActionRecord();
      elements["action-description"].textContent = action
        ? textValue(action.description, "No operation description was provided.")
        : "Available operations come from Orchard’s current capabilities.";
      elements["ipa-field"].hidden = !IPA_ACTIONS.has(state.selectedAction);
      elements["device-field"].hidden = !DEVICE_ACTIONS.has(state.selectedAction);
    }

    function renderActions() {
      const actions = normalizedList(state.server.actions);
      const previousSelection = state.selectedAction;
      elements["action-select"].replaceChildren();

      const prompt = createElement("option", "", actions.length ? "Choose an operation" : "No operations available");
      prompt.value = "";
      elements["action-select"].append(prompt);

      actions.forEach((action) => {
        const option = createElement("option", "", textValue(action.title, action.id));
        option.value = action.id;
        elements["action-select"].append(option);
      });

      state.selectedAction = actions.some((action) => action.id === previousSelection) ? previousSelection : "";
      elements["action-select"].value = state.selectedAction;
      renderActionFields();
    }

    function statusClass(status) {
      const normalized = String(status || "unknown").toLowerCase().replace(/[^a-z0-9-]/g, "-");
      return `status-${normalized}`;
    }

    function renderDiagnostics() {
      const tools = normalizedList(state.server.tools);
      elements["tool-count"].textContent = `${tools.length} ${tools.length === 1 ? "tool" : "tools"} reported`;
      elements["tool-list"].replaceChildren();

      if (tools.length === 0) {
        elements["tool-list"].append(createElement("p", "empty-row", "No tool diagnostics were reported."));
      } else {
        tools.forEach((tool) => {
          const row = createElement("article", "diagnostic-row");
          const name = createElement("div", "diagnostic-name");
          name.append(createElement("h3", "", textValue(tool.name, tool.id || "Unnamed tool")));
          name.append(createElement("p", "", textValue(tool.version, textValue(tool.path, "Version unavailable"))));
          const detail = createElement("p", "diagnostic-detail", textValue(tool.detail, "No diagnostic detail was provided."));
          const status = createElement("span", `status-badge ${statusClass(tool.status)}`, textValue(tool.status, "unknown"));
          name.append(status);
          row.append(name, detail);

          const installUrl = safeHttpsUrl(tool.installUrl);
          if (installUrl) {
            const link = createElement("a", "install-link", "Installation guidance");
            link.href = installUrl;
            link.target = "_blank";
            link.rel = "noopener noreferrer";
            row.append(link);
          }
          elements["tool-list"].append(row);
        });
      }

      const capabilities = normalizedList(state.server.capabilities);
      elements["capability-list"].replaceChildren();
      if (capabilities.length === 0) {
        elements["capability-list"].append(createElement("p", "empty-row", "No capability information was reported."));
      } else {
        capabilities.forEach((capability) => {
          const row = createElement("article", "diagnostic-row");
          const name = createElement("div", "diagnostic-name");
          name.append(createElement("h3", "", textValue(capability.title, capability.id || "Unnamed capability")));
          name.append(createElement("span", `status-badge ${statusClass(capability.status)}`, textValue(capability.status, "unknown")));
          row.append(name, createElement("p", "diagnostic-detail", textValue(capability.detail, "No capability detail was provided.")));
          elements["capability-list"].append(row);
        });
      }
    }

    function displayTimestamp(value) {
      if (typeof value !== "string" || !value) return "Not reported";
      const parsed = new Date(value);
      return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
    }

    function renderResults() {
      elements["result-list"].replaceChildren();
      if (state.results.length === 0) {
        elements["result-list"].append(createElement("p", "empty-row", "No operations have been run in this workspace."));
        return;
      }

      state.results.forEach((result) => {
        const entry = createElement("article", "result-entry");
        const header = createElement("div", "result-header");
        const title = createElement("div");
        title.append(createElement("h2", "", textValue(result.action, "Operation result")));
        title.append(createElement("p", "", `Record ${textValue(result.id, "without an identifier")}`));
        header.append(title, createElement("span", `status-badge ${statusClass(result.status)}`, textValue(result.status, "unknown")));

        const metadata = createElement("div", "result-meta");
        metadata.append(
          createElement("span", "", `Exit code: ${result.exitCode === null || result.exitCode === undefined ? "not reported" : result.exitCode}`),
          createElement("span", "", `Started: ${displayTimestamp(result.startedAt)}`),
          createElement("span", "", `Finished: ${displayTimestamp(result.finishedAt)}`)
        );
        const output = createElement("pre", "log-output", typeof result.output === "string" ? result.output : "No output was returned.");
        entry.append(header, metadata, output);
        elements["result-list"].append(entry);
      });
    }

    function renderState(preferredProject) {
      elements["app-version"].textContent = state.server.version ? `Orchard ${state.server.version}` : "Version unavailable";
      elements["workspace-path"].textContent = textValue(state.server.workspace, "Local workspace");
      renderProjects(preferredProject);
      renderActions();
      renderDiagnostics();
      state.results = normalizedList(state.server.history).slice();
      renderResults();
      invalidatePlan(state.selectedProject ? "Choose an operation to continue." : "Create a project to continue.");
    }

    async function loadState(optionsValue) {
      const loadOptions = optionsValue || {};
      if (state.busy.state) return;
      if (state.disconnected && !loadOptions.retry) return;
      if (loadOptions.retry) {
        if (!token) {
          connectionFailure("This page does not have a session token.");
          return;
        }
        state.disconnected = false;
      }

      state.busy.state = true;
      setConnection("Connecting…", "pending");
      updateControls();
      try {
        const serverState = await apiRequest("/api/state");
        state.server = serverState || {};
        state.disconnected = false;
        clearAlert();
        setConnection("Connected locally", "online");
        renderState(loadOptions.preferredProject || "");
      } catch (error) {
        if (!state.disconnected) elements["plan-status"].textContent = error.message;
      } finally {
        state.busy.state = false;
        updateControls();
      }
    }

    function appendMessages(container, values) {
      container.replaceChildren();
      normalizedList(values).forEach((value) => container.append(createElement("li", "", String(value))));
    }

    function renderPlan(plan) {
      const blockers = normalizedList(plan.blockers);
      const warnings = normalizedList(plan.warnings);
      const steps = normalizedList(plan.steps);
      elements["plan-panel"].hidden = false;
      elements["plan-summary"].textContent = textValue(plan.title, "Operation plan");
      elements["plan-blockers"].hidden = blockers.length === 0;
      elements["plan-warnings"].hidden = warnings.length === 0;
      appendMessages(elements["blocker-list"], blockers);
      appendMessages(elements["warning-list"], warnings);
      elements["plan-steps"].replaceChildren();

      if (steps.length === 0) {
        elements["plan-steps"].append(createElement("p", "empty-row", "This plan has no executable steps."));
      } else {
        steps.forEach((step, index) => {
          const article = createElement("article", "plan-step");
          const header = createElement("div", "plan-step-header");
          const description = createElement("div");
          description.append(createElement("h3", "", `Step ${index + 1}`));
          description.append(createElement("p", "", textValue(step.description, "No step description was provided.")));
          header.append(description, createElement("span", "tool-label", textValue(step.tool, "tool")));
          article.append(header);

          if (step.directory) article.append(createElement("p", "step-directory", `Working directory: ${step.directory}`));

          const scroll = createElement("div", "command-scroll");
          const command = createElement("code", "argv");
          command.setAttribute("aria-label", "Executable followed by individually bounded arguments");
          command.append(createElement("span", "argv-executable", textValue(step.executable, "Executable unavailable")));
          normalizedList(step.args).forEach((argument) => {
            command.append(createElement("span", "argv-boundary", "·"));
            command.append(createElement("span", "argv-argument", String(argument)));
          });
          scroll.append(command, createElement("p", "shell-note", "Argument boxes show process boundaries; this is display only, not shell syntax."));
          article.append(scroll);
          elements["plan-steps"].append(article);
        });
      }

      elements["confirmation-field"].hidden = plan.requiresConfirmation !== true;
      elements["confirm-execution"].checked = false;
      if (blockers.length > 0) {
        elements["plan-status"].textContent = "Plan is blocked. Resolve the listed prerequisites and review again.";
      } else if (plan.executable !== true) {
        elements["plan-status"].textContent = "This plan is informative and cannot be executed.";
      } else if (plan.requiresConfirmation === true) {
        elements["plan-status"].textContent = "Plan is ready after explicit confirmation.";
      } else {
        elements["plan-status"].textContent = "Plan is ready to run.";
      }
      updateControls();
    }

    function planPayload() {
      const body = { action: state.selectedAction, project: state.selectedProject };
      if (!elements["ipa-field"].hidden && elements["ipa-input"].value.trim()) body.ipa = elements["ipa-input"].value.trim();
      if (!elements["device-field"].hidden && elements["device-input"].value.trim()) body.device = elements["device-input"].value.trim();
      return body;
    }

    async function reviewPlan() {
      if (state.busy.plan || anyBusy() || state.disconnected || !state.selectedProject || !state.selectedAction) return;
      const revision = state.inputRevision;
      const requestSequence = ++state.planRequestSequence;
      state.busy.plan = true;
      elements["plan-status"].textContent = "Reviewing operation plan…";
      updateControls();

      try {
        const plan = await apiRequest("/api/plan", { method: "POST", body: planPayload() });
        if (revision !== state.inputRevision || requestSequence !== state.planRequestSequence) return;
        state.currentPlan = plan;
        renderPlan(plan || {});
      } catch (error) {
        if (!state.disconnected) elements["plan-status"].textContent = error.message;
      } finally {
        state.busy.plan = false;
        updateControls();
      }
    }

    function showView(name) {
      const viewNames = ["work", "prerequisites", "results"];
      const selected = viewNames.includes(name) ? name : "work";
      viewNames.forEach((viewName) => {
        const panel = elements[`${viewName}-view`];
        panel.hidden = viewName !== selected;
        panel.classList.toggle("is-active", viewName === selected);
      });
      doc.querySelectorAll(".view-tab").forEach((button) => {
        const active = button.dataset.view === selected;
        button.classList.toggle("is-active", active);
        if (active) button.setAttribute("aria-current", "page");
        else button.removeAttribute("aria-current");
      });
    }

    async function runPlan() {
      const plan = state.currentPlan;
      if (state.busy.run || anyBusy() || state.disconnected || !plan || plan.executable !== true) return;
      if (normalizedList(plan.blockers).length > 0) return;
      const confirmed = elements["confirm-execution"].checked;
      if (plan.requiresConfirmation === true && !confirmed) {
        elements["execution-status"].textContent = "Confirm this operation before running it.";
        elements["confirm-execution"].focus();
        return;
      }

      state.busy.run = true;
      elements["execution-status"].textContent = "Operation running…";
      updateControls();
      try {
        const result = await apiRequest("/api/run", {
          method: "POST",
          body: { planId: plan.id, confirm: confirmed }
        });
        state.results.unshift(result || {});
        renderResults();
        elements["execution-status"].textContent = `Operation ${textValue(result && result.status, "finished")}.`;
        showView("results");
        state.currentPlan = null;
        elements["confirm-execution"].checked = false;
      } catch (error) {
        if (!state.disconnected) elements["execution-status"].textContent = error.message;
      } finally {
        state.busy.run = false;
        updateControls();
      }
    }

    function openCreateDialog() {
      if (state.disconnected || anyBusy()) return;
      state.previousFocus = doc.activeElement;
      elements["create-status"].textContent = "";
      elements["create-project-dialog"].showModal();
      elements["project-name"].focus();
    }

    function closeCreateDialog() {
      if (elements["create-project-dialog"].open) elements["create-project-dialog"].close();
    }

    async function createProject() {
      if (state.busy.create || anyBusy() || state.disconnected) return;
      if (!elements["create-project-form"].reportValidity()) return;
      state.busy.create = true;
      elements["create-status"].textContent = "Creating project…";
      updateControls();
      try {
        const project = await apiRequest("/api/projects", {
          method: "POST",
          body: {
            name: elements["project-name"].value.trim(),
            bundleId: elements["bundle-id"].value.trim(),
            directory: elements["project-directory"].value.trim()
          }
        });
        elements["create-project-form"].reset();
        closeCreateDialog();
        state.busy.create = false;
        await loadState({ preferredProject: project && project.path });
        return;
      } catch (error) {
        if (!state.disconnected) elements["create-status"].textContent = error.message;
      } finally {
        state.busy.create = false;
        updateControls();
      }
    }

    function bindEvents() {
      elements["project-select"].addEventListener("change", () => {
        state.selectedProject = elements["project-select"].value;
        renderProjectDetails();
        invalidatePlan("Project changed. Choose an operation and review a new plan.");
      });

      elements["action-select"].addEventListener("change", () => {
        state.selectedAction = elements["action-select"].value;
        renderActionFields();
        invalidatePlan(state.selectedAction ? "Review this operation before running it." : "Choose an operation to continue.");
      });

      [elements["ipa-input"], elements["device-input"]].forEach((input) => {
        input.addEventListener("input", () => invalidatePlan("Operation inputs changed. Review a new plan before running."));
      });

      elements["confirm-execution"].addEventListener("change", updateControls);
      elements["review-plan"].addEventListener("click", reviewPlan);
      elements["run-plan"].addEventListener("click", runPlan);
      elements["refresh-diagnostics"].addEventListener("click", () => loadState());
      elements["retry-connection"].addEventListener("click", () => loadState({ retry: true }));

      doc.querySelectorAll(".view-tab").forEach((button) => {
        button.addEventListener("click", () => showView(button.dataset.view));
      });

      doc.querySelectorAll("#open-create-project, #header-create-project, #empty-create-project").forEach((button) => {
        button.addEventListener("click", openCreateDialog);
      });

      elements["close-create-project"].addEventListener("click", closeCreateDialog);
      elements["cancel-create-project"].addEventListener("click", closeCreateDialog);
      elements["create-project-form"].addEventListener("submit", (event) => {
        event.preventDefault();
        createProject();
      });
      elements["create-project-dialog"].addEventListener("close", () => {
        if (state.previousFocus && typeof state.previousFocus.focus === "function") state.previousFocus.focus();
        state.previousFocus = null;
      });
    }

    async function start() {
      bindEvents();
      if (!token) {
        connectionFailure("This page does not have a session token.");
        elements["plan-status"].textContent = "Reopen Orchard to start an authenticated session.";
        return;
      }
      await loadState();
    }

    return Object.freeze({
      start,
      loadState,
      reviewPlan,
      runPlan,
      createProject,
      invalidatePlan,
      showView,
      state
    });
  }

  const exported = Object.freeze({ createApp, readSessionToken, safeHttpsUrl });
  root.OrchardWorkspace = exported;

  if (root.document && root.location && root.history) {
    const token = readSessionToken(root.location, root.history);
    const boot = () => {
      const app = createApp({ document: root.document, fetch: root.fetch.bind(root), token });
      root.OrchardWorkspaceApp = app;
      app.start();
    };
    if (root.document.readyState === "loading") root.document.addEventListener("DOMContentLoaded", boot, { once: true });
    else boot();
  }
})(globalThis);
