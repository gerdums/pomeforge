import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../../internal/web/assets/app.js", import.meta.url), "utf8");
const html = await readFile(new URL("../../internal/web/assets/index.html", import.meta.url), "utf8");
const style = await readFile(new URL("../../internal/web/assets/style.css", import.meta.url), "utf8");
const context = vm.createContext({ URL, URLSearchParams });
vm.runInContext(source, context, { filename: "app.js" });
const { createApp, readSessionToken, safeHttpsUrl } = context.OrchardWorkspace;

const elementIds = [
  "connection-status", "project-select", "project-path", "app-version", "workspace-path",
  "app-alert", "alert-title", "alert-message", "retry-connection", "work-view",
  "prerequisites-view", "results-view", "work-title", "project-summary", "empty-projects",
  "work-content", "action-select", "action-description", "ipa-field", "ipa-input",
  "device-field", "device-input", "review-plan", "plan-status", "plan-panel", "plan-summary",
  "plan-blockers", "blocker-list", "plan-warnings", "warning-list", "plan-steps",
  "confirmation-field", "confirm-execution", "run-plan", "execution-status", "refresh-diagnostics",
  "tool-count", "tool-list", "capability-list", "result-list", "create-project-dialog",
  "create-project-form", "project-name", "bundle-id", "project-directory", "create-status",
  "submit-create-project", "close-create-project", "cancel-create-project", "open-create-project",
  "header-create-project", "empty-create-project"
];

class FakeClassList {
  constructor(element) { this.element = element; }
  values() { return new Set(String(this.element.className || "").split(/\s+/).filter(Boolean)); }
  toggle(name, force) {
    const values = this.values();
    const enabled = force === undefined ? !values.has(name) : Boolean(force);
    if (enabled) values.add(name);
    else values.delete(name);
    this.element.className = [...values].join(" ");
    return enabled;
  }
}

class FakeElement {
  constructor(ownerDocument, tagName = "div", id = "") {
    this.ownerDocument = ownerDocument;
    this.tagName = tagName.toUpperCase();
    this.id = id;
    this.children = [];
    this.attributes = new Map();
    this.listeners = new Map();
    this.className = "";
    this.classList = new FakeClassList(this);
    this.dataset = {};
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.open = false;
    this.value = "";
    this.href = "";
    this.target = "";
    this.rel = "";
    this._textContent = "";
  }

  get textContent() { return this._textContent; }
  set textContent(value) {
    this._textContent = String(value);
    this.children = [];
  }
  set innerHTML(_value) { throw new Error("Unsafe HTML insertion attempted"); }
  get innerHTML() { return ""; }

  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = [...nodes]; this._textContent = ""; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  removeAttribute(name) { this.attributes.delete(name); }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  dispatch(type, event = {}) {
    const normalized = { preventDefault() {}, ...event, type, target: this };
    return Promise.all((this.listeners.get(type) || []).map((listener) => listener(normalized)));
  }
  focus() { this.ownerDocument.activeElement = this; }
  showModal() { this.open = true; }
  close() { this.open = false; this.dispatch("close"); }
  reportValidity() { return true; }
  reset() {
    for (const id of ["project-name", "bundle-id", "project-directory"]) this.ownerDocument.getElementById(id).value = "";
  }
}

class FakeDocument {
  constructor() {
    this.elements = new Map(elementIds.map((id) => [id, new FakeElement(this, "div", id)]));
    this.activeElement = null;
    this.viewTabs = ["work", "prerequisites", "results"].map((view) => {
      const button = new FakeElement(this, "button");
      button.dataset.view = view;
      button.className = "view-tab";
      return button;
    });
    this.elements.get("create-project-form").reportValidity = () => true;
    this.elements.get("create-project-form").reset = FakeElement.prototype.reset;
  }
  getElementById(id) { return this.elements.get(id); }
  createElement(tagName) { return new FakeElement(this, tagName); }
  querySelectorAll(selector) {
    if (selector === ".view-tab") return this.viewTabs;
    if (selector === "#open-create-project, #header-create-project, #empty-create-project") {
      return ["open-create-project", "header-create-project", "empty-create-project"].map((id) => this.getElementById(id));
    }
    return [];
  }
}

function apiResponse(data, status = 200) {
  return { ok: status >= 200 && status < 300, status, async json() { return { ok: true, data }; } };
}

function apiError(message, result, status = 500) {
  const error = { code: "operation_failed", message };
  if (result !== undefined) error.result = result;
  return { ok: false, status, async json() { return { ok: false, error }; } };
}

function baseState(overrides = {}) {
  return {
    version: "0.1.0",
    workspace: "/tmp/Orchard Workspace",
    projects: [{ path: "Field Notes", name: "Field Notes", bundleId: "com.example.notes" }],
    tools: [],
    capabilities: [],
    actions: [{ id: "build", title: "Build", description: "Compile this project.", requiresConfirmation: false }],
    history: [],
    ...overrides
  };
}

function createHarness(fetcher, token = "session-secret") {
  const document = new FakeDocument();
  const app = createApp({ document, fetch: fetcher, token });
  return { app, document };
}

function descendants(element) {
  return [element, ...element.children.flatMap((child) => child instanceof FakeElement ? descendants(child) : [])];
}

test("static entrypoint is self-contained and supplies every scripted element", () => {
  assert.doesNotMatch(html, /(?:src|href)=["']https?:/i);
  assert.match(html, /href="\/style\.css"/);
  assert.match(html, /src="\/app\.js"/);
  const referencedIds = [...source.matchAll(/elements\["([a-z0-9-]+)"\]/g)].map((match) => match[1]);
  for (const id of new Set(referencedIds)) assert.match(html, new RegExp(`id="${id}"`), `missing #${id}`);
});

test("the hidden attribute overrides authored layout display rules", () => {
  assert.match(style, /\[hidden\]\s*\{\s*display:\s*none\s*!important;\s*\}/);
  assert.match(style, /\.work-layout\s*\{\s*display:\s*grid/);
  assert.match(style, /\.confirmation\s*\{/);
});

test("session token is removed from the URL and used only as a bearer header", async () => {
  const replacements = [];
  const token = readSessionToken(
    { hash: "#token=secret%20value", pathname: "/", search: "?view=work" },
    { state: { retained: true }, replaceState: (...args) => replacements.push(args) }
  );
  assert.equal(token, "secret value");
  assert.deepEqual(replacements, [[{ retained: true }, "", "/?view=work"]]);

  const requests = [];
  const { app } = createHarness(async (path, init) => {
    requests.push({ path, init });
    return apiResponse(baseState());
  }, token);
  await app.start();
  assert.equal(requests[0].path, "/api/state");
  assert.equal(requests[0].init.headers.Authorization, "Bearer secret value");
  assert.equal(requests[0].init.body, undefined);
});

test("API-controlled text never uses HTML insertion and unsafe install links are omitted", async () => {
  const attack = "<img src=x onerror=alert(1)>";
  const { app, document } = createHarness(async () => apiResponse(baseState({
    projects: [{ path: "unsafe", name: attack, bundleId: attack }],
    tools: [{ id: "xtool", name: attack, status: "missing", detail: attack, installUrl: "javascript:alert(1)" }],
    capabilities: [{ id: "build", title: attack, status: "blocked", detail: attack }]
  })));
  await app.start();
  assert.equal(document.getElementById("work-title").textContent, attack);
  const toolTree = descendants(document.getElementById("tool-list"));
  assert.equal(toolTree.some((element) => element.tagName === "A"), false);
  assert.equal(source.includes(".innerHTML"), false);
  assert.equal(safeHttpsUrl("http://example.com/install"), "");
  assert.equal(safeHttpsUrl("https://example.com/install"), "https://example.com/install");
});

test("project creation posts the contract fields and selects refreshed project state", async () => {
  const requests = [];
  const responses = [
    baseState({ projects: [] }),
    { path: "My App", name: "My App", bundleId: "com.example.myapp" },
    baseState({ projects: [{ path: "My App", name: "My App", bundleId: "com.example.myapp" }] })
  ];
  const { app, document } = createHarness(async (path, init) => {
    requests.push({ path, init });
    return apiResponse(responses.shift());
  });
  await app.start();
  assert.equal(document.getElementById("empty-projects").hidden, false);
  assert.equal(document.getElementById("work-content").hidden, true);
  document.getElementById("project-name").value = "  My App  ";
  document.getElementById("bundle-id").value = " com.example.myapp ";
  document.getElementById("project-directory").value = " My App ";
  await app.createProject();

  assert.equal(requests[1].path, "/api/projects");
  assert.deepEqual(JSON.parse(requests[1].init.body), {
    name: "My App", bundleId: "com.example.myapp", directory: "My App"
  });
  assert.equal(requests[2].path, "/api/state");
  assert.equal(app.state.selectedProject, "My App");
});

test("input invalidation prevents a stale plan response from enabling execution", async () => {
  let resolvePlan;
  const fetcher = async (path) => {
    if (path === "/api/state") return apiResponse(baseState());
    return new Promise((resolve) => { resolvePlan = () => resolve(apiResponse({
      id: "old-plan", action: "build", title: "Build", steps: [], blockers: [], warnings: [],
      requiresConfirmation: false, executable: true
    })); });
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "build";
  const pending = app.reviewPlan();
  app.invalidatePlan("Changed while planning");
  resolvePlan();
  await pending;

  assert.equal(app.state.currentPlan, null);
  assert.equal(document.getElementById("plan-panel").hidden, true);
  assert.equal(document.getElementById("run-plan").disabled, true);
});

test("requesting a replacement plan clears the prior plan even when replanning fails", async () => {
  let planRequests = 0;
  let rejectReplan;
  const fetcher = async (path) => {
    if (path === "/api/state") return apiResponse(baseState());
    planRequests += 1;
    if (planRequests === 1) return apiResponse({
      id: "first-plan", action: "build", title: "Build", steps: [], blockers: [], warnings: [],
      requiresConfirmation: true, executable: true
    });
    return new Promise((resolve) => {
      rejectReplan = () => resolve(apiError("Fresh planning failed."));
    });
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "build";
  await app.reviewPlan();
  document.getElementById("confirm-execution").checked = true;
  await document.getElementById("confirm-execution").dispatch("change");
  assert.equal(document.getElementById("run-plan").disabled, false);

  const pending = app.reviewPlan();
  assert.equal(app.state.currentPlan, null);
  assert.equal(document.getElementById("confirm-execution").checked, false);
  assert.equal(document.getElementById("plan-panel").hidden, true);
  assert.equal(document.getElementById("run-plan").disabled, true);
  rejectReplan();
  await pending;

  assert.equal(app.state.currentPlan, null);
  assert.equal(document.getElementById("run-plan").disabled, true);
  assert.equal(document.getElementById("plan-status").textContent, "Fresh planning failed.");
});

test("plan requests contain only operation inputs and render spaced arguments as boundaries", async () => {
  const requests = [];
  const fetcher = async (path, init) => {
    requests.push({ path, init });
    if (path === "/api/state") return apiResponse(baseState({
      actions: [{ id: "install", title: "Install", description: "Install an IPA.", requiresConfirmation: false }]
    }));
    return apiResponse({
      id: "plan-install", action: "install", title: "Install", blockers: [], warnings: [],
      requiresConfirmation: false, executable: true,
      steps: [{ tool: "xtool", executable: "/opt/xtool", args: ["dev", "My App.ipa"], directory: "Field Notes", description: "Install IPA" }]
    });
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  document.getElementById("action-select").value = "install";
  await document.getElementById("action-select").dispatch("change");
  document.getElementById("ipa-input").value = " dist/My App.ipa ";
  document.getElementById("device-input").value = " device 1 ";
  await app.reviewPlan();

  assert.deepEqual(JSON.parse(requests[1].init.body), {
    action: "install", project: "Field Notes", ipa: "dist/My App.ipa", device: "device 1"
  });
  const argumentsRendered = descendants(document.getElementById("plan-steps"))
    .filter((element) => element.className === "argv-argument")
    .map((element) => element.textContent);
  assert.deepEqual(argumentsRendered, ["dev", "My App.ipa"]);
});

test("confirmation is explicit and duplicate run requests are suppressed", async () => {
  let resolveRun;
  const runRequests = [];
  const fetcher = async (path, init) => {
    if (path === "/api/state") return apiResponse(baseState({
      actions: [{ id: "submit", title: "Submit", description: "Submit for review.", requiresConfirmation: true }]
    }));
    if (path === "/api/plan") return apiResponse({
      id: "plan-7", action: "submit", title: "Submit", steps: [], blockers: [], warnings: [],
      requiresConfirmation: true, executable: true
    });
    runRequests.push(JSON.parse(init.body));
    return new Promise((resolve) => { resolveRun = () => resolve(apiResponse({
      id: "run-7", action: "submit", status: "succeeded", exitCode: 0, output: "submitted",
      startedAt: "2026-09-08T10:00:00Z", finishedAt: "2026-09-08T10:00:01Z"
    })); });
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "submit";
  await app.reviewPlan();
  assert.equal(document.getElementById("run-plan").disabled, true);

  await app.runPlan();
  assert.equal(runRequests.length, 0);
  assert.match(document.getElementById("execution-status").textContent, /Confirm/);

  document.getElementById("confirm-execution").checked = true;
  await document.getElementById("confirm-execution").dispatch("change");
  assert.equal(document.getElementById("run-plan").disabled, false);
  const firstRun = app.runPlan();
  const duplicateRun = app.runPlan();
  assert.equal(runRequests.length, 1);
  assert.deepEqual(runRequests[0], { planId: "plan-7", confirm: true });
  resolveRun();
  await Promise.all([firstRun, duplicateRun]);
  assert.equal(runRequests.length, 1);
});

test("diagnostic refresh merges history with fresh session results by stable ID", async () => {
  const sessionResult = {
    id: "run-session", action: "build", status: "succeeded", exitCode: 0, output: "fresh output",
    startedAt: "2026-09-08T10:00:00Z", finishedAt: "2026-09-08T10:00:01Z"
  };
  const olderResult = {
    id: "run-history", action: "build", status: "failed", exitCode: 1, output: "older output",
    startedAt: "2026-09-08T09:00:00Z", finishedAt: "2026-09-08T09:00:01Z"
  };
  let stateLoads = 0;
  const fetcher = async (path) => {
    if (path === "/api/state") {
      stateLoads += 1;
      if (stateLoads === 1) return apiResponse(baseState({ history: [olderResult] }));
      if (stateLoads === 2) return apiResponse(baseState({ history: [] }));
      return apiResponse(baseState({ history: [
        { ...sessionResult, output: "stale output" },
        { ...olderResult, output: "updated backend output" }
      ] }));
    }
    if (path === "/api/plan") return apiResponse({
      id: "plan-session", action: "build", title: "Build", steps: [], blockers: [], warnings: [],
      requiresConfirmation: false, executable: true
    });
    return apiResponse(sessionResult);
  };
  const { app } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "build";
  await app.reviewPlan();
  await app.runPlan();
  assert.deepEqual(Array.from(app.state.results, (result) => result.id), ["run-session", "run-history"]);

  await app.loadState();
  assert.deepEqual(Array.from(app.state.results, (result) => result.id), ["run-session"]);
  assert.equal(app.state.results[0].output, "fresh output");

  await app.loadState();
  assert.deepEqual(Array.from(app.state.results, (result) => result.id), ["run-session", "run-history"]);
  assert.equal(app.state.results[0].output, "fresh output");
  assert.equal(app.state.results[1].output, "updated backend output");
});

test("a result-bearing run failure is logged and consumes the executed plan", async () => {
  const failedResult = {
    id: "run-failed", action: "build", status: "failed", exitCode: 17, output: "compiler failed safely",
    startedAt: "2026-09-08T10:00:00Z", finishedAt: "2026-09-08T10:00:02Z"
  };
  const fetcher = async (path) => {
    if (path === "/api/state") return apiResponse(baseState());
    if (path === "/api/plan") return apiResponse({
      id: "plan-failed", action: "build", title: "Build", steps: [], blockers: [], warnings: [],
      requiresConfirmation: false, executable: true
    });
    return apiError("The launched operation failed.", failedResult, 422);
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "build";
  await app.reviewPlan();
  await app.runPlan();

  assert.equal(app.state.currentPlan, null);
  assert.equal(app.state.results.length, 1);
  assert.equal(app.state.results[0], failedResult);
  assert.equal(document.getElementById("alert-message").textContent, "The launched operation failed.");
  assert.equal(document.getElementById("results-view").hidden, false);
  const resultText = descendants(document.getElementById("result-list")).map((element) => element.textContent);
  assert.ok(resultText.includes("failed"));
  assert.ok(resultText.includes("Exit code: 17"));
  assert.ok(resultText.includes("compiler failed safely"));
});

test("an ordinary run error shows the error without inventing a result", async () => {
  const fetcher = async (path) => {
    if (path === "/api/state") return apiResponse(baseState());
    if (path === "/api/plan") return apiResponse({
      id: "plan-retryable", action: "build", title: "Build", steps: [], blockers: [], warnings: [],
      requiresConfirmation: false, executable: true
    });
    return apiError("The plan was not launched.", undefined, 409);
  };
  const { app, document } = createHarness(fetcher);
  await app.start();
  app.state.selectedAction = "build";
  await app.reviewPlan();
  await app.runPlan();

  assert.equal(app.state.currentPlan.id, "plan-retryable");
  assert.equal(app.state.results.length, 0);
  assert.equal(document.getElementById("alert-message").textContent, "The plan was not launched.");
  assert.equal(document.getElementById("execution-status").textContent, "The plan was not launched.");
});

test("transport failure disables requests until an explicit retry succeeds", async () => {
  let calls = 0;
  const { app, document } = createHarness(async () => {
    calls += 1;
    if (calls === 1) throw new Error("offline");
    return apiResponse(baseState());
  });
  await app.start();
  assert.equal(app.state.disconnected, true);
  assert.equal(document.getElementById("review-plan").disabled, true);
  assert.equal(document.getElementById("retry-connection").hidden, false);
  await app.reviewPlan();
  assert.equal(calls, 1);

  await app.loadState({ retry: true });
  assert.equal(calls, 2);
  assert.equal(app.state.disconnected, false);
  assert.equal(document.getElementById("app-alert").hidden, true);
});
