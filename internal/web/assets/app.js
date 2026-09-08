(function (root) {
  "use strict";

  const IPA_ACTIONS = new Set(["install", "upload", "ipa-inspect"]);
  const DEVICE_ACTIONS = new Set(["install", "launch"]);
  const IDENTITY_ACTIONS = new Set(["export", "upload", "ipa-inspect"]);
  const SOURCE_BUNDLE_ACTIONS = new Set(["release-build", "export"]);
  const APP_ID_ACTIONS = new Set(["upload", "validate", "submit"]);
  const VERSION_ID_ACTIONS = new Set(["validate", "submit"]);
  const BUILD_ID_ACTIONS = new Set(["store-status", "submit"]);

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
      "source-bundle-field", "source-bundle-input", "output-ipa-field", "output-ipa-input",
      "asset-catalog-field", "asset-catalog-input", "icon-source-field", "icon-source-input",
      "identity-field", "identity-select", "app-id-field", "app-id-input", "version-id-field",
      "version-id-input", "build-id-field", "build-id-input", "version-field", "version-input",
      "plan-blockers", "blocker-list", "plan-warnings", "warning-list", "plan-steps",
      "confirmation-field", "confirm-execution", "run-plan", "execution-status", "refresh-diagnostics",
      "tool-count", "tool-list", "capability-list", "result-list", "create-project-dialog",
      "create-project-form", "project-name", "bundle-id", "project-directory", "create-status",
      "submit-create-project", "close-create-project", "cancel-create-project"
	  , "empty-open-setup", "setup-action-select", "setup-tool-field", "setup-tool-select",
      "setup-helper-field", "setup-helper-select", "setup-helper-path-field", "setup-helper-path",
      "setup-source-revision-field", "setup-source-revision", "setup-assetkit-revision-field",
      "setup-assetkit-revision", "setup-sdk-input-field", "setup-sdk-input", "setup-sdk-arch-field",
      "setup-sdk-arch", "review-setup-plan", "setup-plan-status", "setup-plan-panel", "setup-plan-title",
      "setup-plan-blockers", "setup-blocker-list", "setup-plan-warnings", "setup-warning-list",
      "setup-plan-steps", "setup-confirmation-field", "confirm-setup-execution", "run-setup-plan",
      "setup-execution-status", "setup-identity-id-field", "setup-identity-id",
      "setup-identity-label-field", "setup-identity-label", "setup-private-key-field",
      "setup-private-key", "setup-certificate-field", "setup-certificate", "setup-profile-field",
      "setup-profile", "setup-trust-roots-field", "setup-trust-roots", "identity-list"
    ];

    ids.forEach((id) => { elements[id] = doc.getElementById(id); });

    const state = {
      server: null,
      selectedProject: "",
      selectedAction: "",
      currentPlan: null,
	  selectedSetupAction: "",
      currentSetupPlan: null,
	  setupInputRevision: 0,
      setupRequestSequence: 0,
      inputRevision: 0,
      planRequestSequence: 0,
      disconnected: false,
	  busy: { state: false, plan: false, run: false, create: false, setupPlan: false, setupRun: false },
      previousFocus: null,
      sessionResults: [],
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
        "Pomeforge connection unavailable",
        `${message} Retry this connection, or close this tab and reopen Pomeforge from the desktop launcher or with “pomeforge app --open”.`,
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
        connectionFailure("The local Pomeforge process did not respond.");
        throw new Error("The local Pomeforge process did not respond.");
      }

      let payload;
      try {
        payload = await response.json();
      } catch (_error) {
        showAlert("Unexpected response", "Pomeforge returned a response that was not valid JSON.", false);
        throw new Error("Pomeforge returned invalid JSON.");
      }

      if (!response.ok || !payload || payload.ok !== true) {
        const apiMessage = payload && payload.error && typeof payload.error.message === "string"
          ? payload.error.message
          : `Pomeforge returned HTTP ${response.status}.`;
        if (response.status === 401 || response.status === 403) {
          connectionFailure(`${apiMessage} The session may have expired.`);
        } else {
          showAlert("Request failed", apiMessage, false);
        }
        const apiError = new Error(apiMessage);
        if (payload && payload.error && payload.error.result && typeof payload.error.result === "object" && !Array.isArray(payload.error.result)) {
          apiError.result = payload.error.result;
        }
        throw apiError;
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
      ["source-bundle-input", "output-ipa-input", "asset-catalog-input", "icon-source-input",
        "identity-select", "app-id-input", "version-id-input", "build-id-input", "version-input"].forEach((id) => {
        elements[id].disabled = unavailable;
      });
      elements["review-plan"].disabled = unavailable || !state.selectedProject || !state.selectedAction;
      elements["run-plan"].disabled = unavailable || !plan || plan.executable !== true || blockers.length > 0 || !confirmationSatisfied;
      elements["refresh-diagnostics"].disabled = unavailable;
      elements["submit-create-project"].disabled = unavailable;
	  elements["setup-action-select"].disabled = unavailable;
      elements["setup-tool-select"].disabled = unavailable;
      elements["setup-helper-select"].disabled = unavailable;
      elements["setup-helper-path"].disabled = unavailable;
      elements["setup-source-revision"].disabled = unavailable;
      elements["setup-assetkit-revision"].disabled = unavailable;
      elements["setup-sdk-input"].disabled = unavailable;
      elements["setup-sdk-arch"].disabled = unavailable;
      ["setup-identity-id", "setup-identity-label", "setup-private-key", "setup-certificate",
        "setup-profile", "setup-trust-roots"].forEach((id) => { elements[id].disabled = unavailable; });
      elements["review-setup-plan"].disabled = unavailable || !state.selectedSetupAction || !setupInputsComplete();
      const setupPlan = state.currentSetupPlan;
      const setupBlockers = setupPlan ? normalizedList(setupPlan.blockers) : [];
      const setupConfirmed = !setupPlan || !setupPlan.requiresConfirmation || elements["confirm-setup-execution"].checked;
      elements["run-setup-plan"].disabled = unavailable || !setupPlan || setupPlan.executable !== true || setupBlockers.length > 0 || !setupConfirmed;

	  elements["empty-open-setup"].disabled = unavailable;
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
        : "Available operations come from Pomeforge’s current capabilities.";
      elements["ipa-field"].hidden = !IPA_ACTIONS.has(state.selectedAction);
      elements["device-field"].hidden = !DEVICE_ACTIONS.has(state.selectedAction);
      elements["source-bundle-field"].hidden = !SOURCE_BUNDLE_ACTIONS.has(state.selectedAction);
      elements["output-ipa-field"].hidden = state.selectedAction !== "export";
      elements["asset-catalog-field"].hidden = state.selectedAction !== "export";
      elements["icon-source-field"].hidden = state.selectedAction !== "icons";
      elements["identity-field"].hidden = !IDENTITY_ACTIONS.has(state.selectedAction);
      elements["app-id-field"].hidden = !APP_ID_ACTIONS.has(state.selectedAction);
      elements["version-id-field"].hidden = !VERSION_ID_ACTIONS.has(state.selectedAction);
      elements["build-id-field"].hidden = !BUILD_ID_ACTIONS.has(state.selectedAction);
      elements["version-field"].hidden = state.selectedAction !== "validate";
    }

    function renderActions() {
	  const actions = normalizedList(state.server.actions).filter((action) => action.scope !== "workspace");
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

    function renderIdentities() {
      const identities = normalizedList(state.server.identities);
      elements["identity-select"].replaceChildren();
      const prompt = createElement("option", "", identities.length ? "Choose an identity" : "No identities configured");
      prompt.value = "";
      elements["identity-select"].append(prompt);
      elements["identity-list"].replaceChildren();
      if (!identities.length) {
        elements["identity-list"].append(createElement("p", "empty-row", "Configure a named identity using the setup operation below."));
        return;
      }
      identities.forEach((identity) => {
        const option = createElement("option", "", textValue(identity.label, identity.id));
        option.value = identity.id;
        elements["identity-select"].append(option);
        const row = createElement("article", "diagnostic-row");
        const name = createElement("div", "diagnostic-name");
        name.append(createElement("h3", "", textValue(identity.label, identity.id)));
        name.append(createElement("p", "", `Identity ${textValue(identity.id, "unknown")}`));
        name.append(createElement("span", `status-badge ${statusClass(identity.status)}`, textValue(identity.status, "unknown")));
        const report = identity.report || {};
        const certificate = report.certificate || {};
        const profile = report.profile || {};
        const detail = identity.error || [
          `certificate ${textValue(certificate.subjectCN, "subject unavailable")}`,
          `issuer ${textValue(certificate.issuerCN, "unavailable")}`,
          `fingerprint ${textValue(certificate.sha256Fingerprint, "unavailable")}`,
          `profile ${textValue(profile.name, "name unavailable")} (${textValue(profile.type, "type unavailable")})`,
          `bundle ${textValue(profile.bundleIdentifier, "unavailable")}`,
          `expires ${displayTimestamp(profile.expirationDate)}`,
          `trust ${textValue(profile.trust, "not reported")}`
        ].join(" · ");
        row.append(name, createElement("p", "diagnostic-detail", detail));
        elements["identity-list"].append(row);
      });
    }

    function setupInputsComplete() {
      switch (state.selectedSetupAction) {
      case "tool-install": return Boolean(elements["setup-tool-select"].value);
      case "helper-register": return Boolean(elements["setup-helper-select"].value && elements["setup-helper-path"].value.trim());
      case "sdk-status": return true;
      case "sdk-import": return Boolean(elements["setup-sdk-input"].value.trim() && elements["setup-sdk-arch"].value);
      case "signing-configure": return Boolean(elements["setup-identity-id"].value.trim() && elements["setup-identity-label"].value.trim() && elements["setup-private-key"].value.trim() && elements["setup-certificate"].value.trim() && elements["setup-profile"].value.trim());
      case "signing-inspect": return Boolean(elements["setup-identity-id"].value.trim());
      default: return false;
      }
    }

    function renderSetupActions() {
      const actions = normalizedList(state.server.actions).filter((action) => action.scope === "workspace");
      elements["setup-action-select"].replaceChildren();
      const prompt = createElement("option", "", actions.length ? "Choose a setup operation" : "No setup operations available");
      prompt.value = "";
      elements["setup-action-select"].append(prompt);
      actions.forEach((action) => {
        const option = createElement("option", "", textValue(action.title, action.id));
        option.value = action.id;
        elements["setup-action-select"].append(option);
      });
      if (!actions.some((action) => action.id === state.selectedSetupAction)) state.selectedSetupAction = "";
      elements["setup-action-select"].value = state.selectedSetupAction;
      renderSetupFields();
    }

    function renderSetupFields() {
      const action = state.selectedSetupAction;
      elements["setup-tool-field"].hidden = action !== "tool-install";
      const helper = action === "helper-register";
      elements["setup-helper-field"].hidden = !helper;
      elements["setup-helper-path-field"].hidden = !helper;
      elements["setup-source-revision-field"].hidden = !helper;
      elements["setup-assetkit-revision-field"].hidden = !helper || elements["setup-helper-select"].value !== "pomeforge-assets";
      elements["setup-sdk-input-field"].hidden = action !== "sdk-import";
      elements["setup-sdk-arch-field"].hidden = action !== "sdk-import";
      const signing = action === "signing-configure" || action === "signing-inspect";
      const configuring = action === "signing-configure";
      elements["setup-identity-id-field"].hidden = !signing;
      elements["setup-identity-label-field"].hidden = !configuring;
      elements["setup-private-key-field"].hidden = !configuring;
      elements["setup-certificate-field"].hidden = !configuring;
      elements["setup-profile-field"].hidden = !configuring;
      elements["setup-trust-roots-field"].hidden = !configuring;
    }

    function invalidateSetupPlan(message) {
      state.setupInputRevision += 1;
      state.setupRequestSequence += 1;
      state.currentSetupPlan = null;
      elements["confirm-setup-execution"].checked = false;
      elements["setup-plan-panel"].hidden = true;
      elements["setup-execution-status"].textContent = "";
      elements["setup-plan-status"].textContent = message || "Setup inputs changed. Review a new plan before running.";
      updateControls();
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
	  const visibleResults = state.results.filter((result) => !result || result.scope === "workspace" || !result.scope || result.project === state.selectedProject);
      if (visibleResults.length === 0) {
        elements["result-list"].append(createElement("p", "empty-row", "No operations have been run in this workspace."));
        return;
      }

	  visibleResults.forEach((result) => {
        const entry = createElement("article", "result-entry");
        const header = createElement("div", "result-header");
        const title = createElement("div");
        title.append(createElement("h2", "", textValue(result.action, "Operation result")));
		const scopeLabel = result.scope === "workspace"
          ? "Workspace setup"
          : result.scope === "project"
            ? `${textValue(result.projectLabel, "Project")} · ${textValue(result.project, "unknown path")}`
            : "Legacy unscoped record";
        title.append(createElement("p", "", `${scopeLabel} · Record ${textValue(result.id, "without an identifier")}`));
        header.append(title, createElement("span", `status-badge ${statusClass(result.status)}`, textValue(result.status, "unknown")));

        const metadata = createElement("div", "result-meta");
        metadata.append(
          createElement("span", "", `Exit code: ${result.exitCode === null || result.exitCode === undefined ? "not reported" : result.exitCode}`),
          createElement("span", "", `Started: ${displayTimestamp(result.startedAt)}`),
          createElement("span", "", `Finished: ${displayTimestamp(result.finishedAt)}`)
        );
        const output = createElement("pre", "log-output", typeof result.output === "string" ? result.output : "No output was returned.");
		entry.append(header, metadata);
        if (result.metadata && typeof result.metadata === "object" && !Array.isArray(result.metadata)) {
          entry.append(createElement("pre", "result-metadata", JSON.stringify(result.metadata, null, 2)));
        }
        entry.append(output);
        elements["result-list"].append(entry);
      });
    }

    function mergeResultRecords(primary, secondary) {
      const merged = [];
      const resultIds = new Set();
      [primary, secondary].forEach((records) => {
        normalizedList(records).forEach((record) => {
          if (!record || typeof record !== "object" || Array.isArray(record)) return;
          const id = typeof record.id === "string" && record.id.length > 0 ? record.id : "";
          if (id && resultIds.has(id)) return;
          if (id) resultIds.add(id);
          merged.push(record);
        });
      });
      return merged;
    }

    function renderState(preferredProject) {
      elements["app-version"].textContent = state.server.version ? `Pomeforge ${state.server.version}` : "Version unavailable";
      elements["workspace-path"].textContent = textValue(state.server.workspace, "Local workspace");
      renderProjects(preferredProject);
      renderActions();
	  renderSetupActions();
      renderIdentities();
      renderDiagnostics();
      state.results = mergeResultRecords(state.sessionResults, state.server.history);
      renderResults();
      invalidatePlan(state.selectedProject ? "Choose an operation to continue." : "Create a project to continue.");
	  invalidateSetupPlan(state.selectedSetupAction ? "Review this setup operation before running." : "Choose a setup operation.");
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
          if (step.kind === "internal") {
            command.setAttribute("aria-label", "Structured Pomeforge internal operation");
            command.append(createElement("span", "argv-executable", `Pomeforge: ${textValue(step.operation, "internal operation")}`));
            Object.keys(step.parameters || {}).sort().forEach((key) => {
              command.append(createElement("span", "argv-boundary", "·"));
              command.append(createElement("span", "argv-argument", `${key}=${step.parameters[key]}`));
            });
            scroll.append(command, createElement("p", "shell-note", "This is a structured internal operation, not a fabricated shell command."));
          } else {
            command.setAttribute("aria-label", "Executable followed by individually bounded arguments");
            command.append(createElement("span", "argv-executable", textValue(step.executable, "Executable unavailable")));
            normalizedList(step.args).forEach((argument) => {
              command.append(createElement("span", "argv-boundary", "·"));
              command.append(createElement("span", "argv-argument", String(argument)));
            });
            scroll.append(command, createElement("p", "shell-note", "Argument boxes show process boundaries; this is display only, not shell syntax."));
          }
          article.append(scroll);
          elements["plan-steps"].append(article);
        });
      }

      ["sdkBinding", "identityInspection", "ipaInspection", "distributionExport"].forEach((key) => {
        if (plan[key] && typeof plan[key] === "object") {
          elements["plan-steps"].append(createElement("pre", "result-metadata", JSON.stringify(plan[key], null, 2)));
        }
      });

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

    function renderSetupPlan(plan) {
      const blockers = normalizedList(plan.blockers);
      const warnings = normalizedList(plan.warnings);
      elements["setup-plan-panel"].hidden = false;
      elements["setup-plan-title"].textContent = textValue(plan.title, "Setup plan");
      elements["setup-plan-blockers"].hidden = blockers.length === 0;
      elements["setup-plan-warnings"].hidden = warnings.length === 0;
      appendMessages(elements["setup-blocker-list"], blockers);
      appendMessages(elements["setup-warning-list"], warnings);
      elements["setup-plan-steps"].replaceChildren();
      normalizedList(plan.steps).forEach((step, index) => {
        const article = createElement("article", "plan-step");
        const header = createElement("div", "plan-step-header");
        const description = createElement("div");
        description.append(createElement("h3", "", `Step ${index + 1}`));
        description.append(createElement("p", "", textValue(step.description, "No step description was provided.")));
        header.append(description, createElement("span", "tool-label", textValue(step.tool, "tool")));
        article.append(header);
        const scroll = createElement("div", "command-scroll");
        const command = createElement("code", "argv");
        if (step.kind === "internal") {
          command.setAttribute("aria-label", "Structured Pomeforge internal operation");
          command.append(createElement("span", "argv-executable", `Pomeforge: ${textValue(step.operation, "internal operation")}`));
          Object.keys(step.parameters || {}).sort().forEach((key) => {
            command.append(createElement("span", "argv-boundary", "·"));
            command.append(createElement("span", "argv-argument", `${key}=${step.parameters[key]}`));
          });
          scroll.append(command, createElement("p", "shell-note", "This is a structured internal operation, not a fabricated shell command."));
        } else {
          command.setAttribute("aria-label", "Executable followed by individually bounded arguments");
          command.append(createElement("span", "argv-executable", textValue(step.executable, "Executable unavailable")));
          normalizedList(step.args).forEach((argument) => {
            command.append(createElement("span", "argv-boundary", "·"));
            command.append(createElement("span", "argv-argument", String(argument)));
          });
          scroll.append(command, createElement("p", "shell-note", "Argument boxes show process boundaries; this is display only, not shell syntax."));
        }
        article.append(scroll);
        elements["setup-plan-steps"].append(article);
      });
      elements["setup-confirmation-field"].hidden = plan.requiresConfirmation !== true;
      elements["confirm-setup-execution"].checked = false;
      elements["setup-plan-status"].textContent = blockers.length
        ? "Setup plan is blocked. Resolve the listed prerequisites and review again."
        : plan.executable === true ? "Setup plan is ready to run." : "This setup plan cannot be executed.";
      updateControls();
    }

    function setupPlanPayload() {
      const body = { action: state.selectedSetupAction };
      if (state.selectedSetupAction === "tool-install") body.tool = elements["setup-tool-select"].value;
      if (state.selectedSetupAction === "helper-register") {
        body.helper = elements["setup-helper-select"].value;
        body.executablePath = elements["setup-helper-path"].value.trim();
        if (elements["setup-source-revision"].value.trim()) body.sourceRevision = elements["setup-source-revision"].value.trim();
		if (body.helper === "pomeforge-assets" && elements["setup-assetkit-revision"].value.trim()) body.assetKitRevision = elements["setup-assetkit-revision"].value.trim();
      }
      if (state.selectedSetupAction === "sdk-import") {
        body.inputPath = elements["setup-sdk-input"].value.trim();
        body.arch = elements["setup-sdk-arch"].value;
      }
      if (state.selectedSetupAction === "signing-configure" || state.selectedSetupAction === "signing-inspect") {
        body.identity = elements["setup-identity-id"].value.trim();
      }
      if (state.selectedSetupAction === "signing-configure") {
        body.identityLabel = elements["setup-identity-label"].value.trim();
        body.privateKeyPath = elements["setup-private-key"].value.trim();
        body.certificatePath = elements["setup-certificate"].value.trim();
        body.profilePath = elements["setup-profile"].value.trim();
        const roots = elements["setup-trust-roots"].value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean);
        if (roots.length) body.trustedRootPaths = roots;
      }
      return body;
    }

    async function reviewSetupPlan() {
      if (state.busy.setupPlan || anyBusy() || state.disconnected || !state.selectedSetupAction || !setupInputsComplete()) return;
      invalidateSetupPlan("Reviewing setup plan…");
      const revision = state.setupInputRevision;
      const requestSequence = state.setupRequestSequence;
      state.busy.setupPlan = true;
      updateControls();
      try {
        const plan = await apiRequest("/api/plan", { method: "POST", body: setupPlanPayload() });
        if (revision !== state.setupInputRevision || requestSequence !== state.setupRequestSequence) return;
        state.currentSetupPlan = plan;
        renderSetupPlan(plan || {});
      } catch (error) {
        if (!state.disconnected) elements["setup-plan-status"].textContent = error.message;
      } finally {
        state.busy.setupPlan = false;
        updateControls();
      }
    }

    async function runSetupPlan() {
      const plan = state.currentSetupPlan;
      if (state.busy.setupRun || anyBusy() || state.disconnected || !plan || plan.executable !== true || normalizedList(plan.blockers).length) return;
      const confirmed = elements["confirm-setup-execution"].checked;
      if (plan.requiresConfirmation === true && !confirmed) return;
      state.busy.setupRun = true;
      elements["setup-execution-status"].textContent = "Setup operation running…";
      updateControls();
      try {
        const result = await apiRequest("/api/run", { method: "POST", body: { planId: plan.id, confirm: confirmed } });
        state.sessionResults = mergeResultRecords(result && typeof result === "object" ? [result] : [], state.sessionResults);
        state.results = mergeResultRecords(state.sessionResults, state.results);
        renderResults();
        state.currentSetupPlan = null;
        elements["setup-plan-panel"].hidden = true;
		elements["confirm-setup-execution"].checked = false;
		const outcome = `Setup operation ${textValue(result && result.status, "finished")}.`;
        state.busy.setupRun = false;
        await loadState();
        showView("prerequisites");
		elements["setup-execution-status"].textContent = outcome;
        return;
      } catch (error) {
        const refreshDiagnostics = Boolean(error.result);
        if (error.result) {
          state.sessionResults = mergeResultRecords([error.result], state.sessionResults);
          state.results = mergeResultRecords(state.sessionResults, state.results);
          renderResults();
        }
		state.currentSetupPlan = null;
		elements["confirm-setup-execution"].checked = false;
		elements["setup-plan-panel"].hidden = true;
        if (refreshDiagnostics && !state.disconnected) {
          state.busy.setupRun = false;
          await loadState();
          showView("prerequisites");
        }
        if (!state.disconnected) elements["setup-execution-status"].textContent = error.message;
      } finally {
        state.busy.setupRun = false;
        updateControls();
      }
    }

    function planPayload() {
      const body = { action: state.selectedAction, project: state.selectedProject };
      if (!elements["ipa-field"].hidden && elements["ipa-input"].value.trim()) body.ipa = elements["ipa-input"].value.trim();
      if (!elements["device-field"].hidden && elements["device-input"].value.trim()) body.device = elements["device-input"].value.trim();
      const fields = [
        ["source-bundle-field", "source-bundle-input", "sourceBundle"],
        ["output-ipa-field", "output-ipa-input", "outputIPA"],
        ["asset-catalog-field", "asset-catalog-input", "assetCatalog"],
        ["icon-source-field", "icon-source-input", "iconSource"],
        ["identity-field", "identity-select", "identity"],
        ["app-id-field", "app-id-input", "appId"],
        ["version-id-field", "version-id-input", "versionId"],
        ["build-id-field", "build-id-input", "buildId"],
        ["version-field", "version-input", "version"]
      ];
      fields.forEach(([container, input, key]) => {
        if (!elements[container].hidden && elements[input].value.trim()) body[key] = elements[input].value.trim();
      });
      return body;
    }

    async function reviewPlan() {
      if (state.busy.plan || anyBusy() || state.disconnected || !state.selectedProject || !state.selectedAction) return;
      invalidatePlan("Reviewing operation plan…");
      const revision = state.inputRevision;
      const requestSequence = state.planRequestSequence;
      state.busy.plan = true;
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
        state.sessionResults = mergeResultRecords(result && typeof result === "object" ? [result] : [], state.sessionResults);
        state.results = mergeResultRecords(state.sessionResults, state.results);
        renderResults();
        elements["execution-status"].textContent = `Operation ${textValue(result && result.status, "finished")}.`;
        showView("results");
        state.currentPlan = null;
        elements["confirm-execution"].checked = false;
      } catch (error) {
        if (error.result) {
          state.sessionResults = mergeResultRecords([error.result], state.sessionResults);
          state.results = mergeResultRecords(state.sessionResults, state.results);
          renderResults();
          state.currentPlan = null;
          elements["confirm-execution"].checked = false;
          elements["plan-panel"].hidden = true;
          showView("results");
        }
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
		renderResults();
        invalidatePlan("Project changed. Choose an operation and review a new plan.");
      });

      elements["action-select"].addEventListener("change", () => {
        state.selectedAction = elements["action-select"].value;
        renderActionFields();
        invalidatePlan(state.selectedAction ? "Review this operation before running it." : "Choose an operation to continue.");
      });

      ["ipa-input", "device-input", "source-bundle-input", "output-ipa-input", "asset-catalog-input",
        "icon-source-input", "identity-select", "app-id-input", "version-id-input", "build-id-input",
        "version-input"].map((id) => elements[id]).forEach((input) => {
        input.addEventListener("input", () => invalidatePlan("Operation inputs changed. Review a new plan before running."));
        input.addEventListener("change", () => invalidatePlan("Operation inputs changed. Review a new plan before running."));
      });

      elements["confirm-execution"].addEventListener("change", updateControls);
      elements["review-plan"].addEventListener("click", reviewPlan);
      elements["run-plan"].addEventListener("click", runPlan);
	  elements["setup-action-select"].addEventListener("change", () => {
        state.selectedSetupAction = elements["setup-action-select"].value;
        renderSetupFields();
        invalidateSetupPlan(state.selectedSetupAction ? "Review this setup operation before running." : "Choose a setup operation.");
      });
      elements["setup-helper-select"].addEventListener("change", () => {
        renderSetupFields();
        invalidateSetupPlan("Setup inputs changed. Review a new plan before running.");
      });
      ["setup-tool-select", "setup-helper-path", "setup-source-revision", "setup-assetkit-revision", "setup-sdk-input", "setup-sdk-arch",
        "setup-identity-id", "setup-identity-label", "setup-private-key", "setup-certificate", "setup-profile", "setup-trust-roots"].forEach((id) => {
        elements[id].addEventListener("input", () => invalidateSetupPlan("Setup inputs changed. Review a new plan before running."));
        elements[id].addEventListener("change", () => invalidateSetupPlan("Setup inputs changed. Review a new plan before running."));
      });
      elements["confirm-setup-execution"].addEventListener("change", updateControls);
      elements["review-setup-plan"].addEventListener("click", reviewSetupPlan);
      elements["run-setup-plan"].addEventListener("click", runSetupPlan);
	  elements["empty-open-setup"].addEventListener("click", () => {
        showView("prerequisites");
        elements["setup-action-select"].focus();
      });
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
        elements["plan-status"].textContent = "Reopen Pomeforge to start an authenticated session.";
        return;
      }
      await loadState();
    }

    return Object.freeze({
      start,
      loadState,
      reviewPlan,
      runPlan,
	  reviewSetupPlan,
      runSetupPlan,
      createProject,
      invalidatePlan,
	  invalidateSetupPlan,
      showView,
      state
    });
  }

  const exported = Object.freeze({ createApp, readSessionToken, safeHttpsUrl });
  root.PomeforgeWorkspace = exported;

  if (root.document && root.location && root.history) {
    const token = readSessionToken(root.location, root.history);
    const boot = () => {
      const app = createApp({ document: root.document, fetch: root.fetch.bind(root), token });
      root.PomeforgeWorkspaceApp = app;
      app.start();
    };
    if (root.document.readyState === "loading") root.document.addEventListener("DOMContentLoaded", boot, { once: true });
    else boot();
  }
})(globalThis);
