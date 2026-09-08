import assert from "node:assert/strict";
import { access, chmod, mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const packagingDir = path.resolve(new URL("..", import.meta.url).pathname);

async function readEventually(filePath) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      const contents = await readFile(filePath, "utf8");
      if (contents.length > 0) return contents;
    } catch (error) {
      if (error.code !== "ENOENT" || attempt === 49) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`Timed out reading ${filePath}`);
}

test("GIO interprets the installed desktop entry with special path characters", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "orchard package test "));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });

  const home = path.join(root, "user home");
  const dataHome = path.join(root, "xdg & | ' \" \\ $ ` data");
  const sourceDir = path.join(root, "prebuilt files");
  const sourceBinary = path.join(sourceDir, "orchard build");
  const argumentLog = path.join(root, "arguments.log");
  await mkdir(home, { recursive: true });
  await mkdir(sourceDir, { recursive: true });
  await mkdir(path.join(dataHome, "orchard"), { recursive: true });
  await writeFile(path.join(dataHome, "orchard", "unrelated.txt"), "preserve me\n");
  await writeFile(sourceBinary, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ORCHARD_ARG_LOG\"\n");
  await chmod(sourceBinary, 0o755);

  const environment = { ...process.env, HOME: home, XDG_DATA_HOME: dataHome, ORCHARD_ARG_LOG: argumentLog };
  const installResult = spawnSync(path.join(packagingDir, "install.sh"), [sourceBinary], {
    env: environment, encoding: "utf8"
  });
  assert.equal(installResult.status, 0, installResult.stderr);
  assert.equal(await readFile(path.join(dataHome, "orchard", "unrelated.txt"), "utf8"), "preserve me\n");

  const desktopPath = path.join(dataHome, "applications", "orchard.desktop");
  const desktop = await readFile(desktopPath, "utf8");
  assert.match(desktop, /^Exec=".+"$/m);

  const workspace = path.join(root, "Projects 100% & | ' \" \\ $ ` ready");
  const launchResult = spawnSync(path.join(dataHome, "orchard", "bin", "orchard-workspace"), [], {
    env: { ...environment, ORCHARD_WORKSPACE: workspace }, encoding: "utf8"
  });
  assert.equal(launchResult.status, 0, launchResult.stderr);
  const argumentsSeen = (await readFile(argumentLog, "utf8")).trimEnd().split("\n");
  assert.deepEqual(argumentsSeen, ["app", "--open", "--workspace", workspace, "--listen", "127.0.0.1:0"]);

  await writeFile(argumentLog, "");
  const gioResult = spawnSync("gio", ["launch", desktopPath], {
    env: { ...environment, ORCHARD_WORKSPACE: workspace }, encoding: "utf8"
  });
  assert.equal(gioResult.status, 0, `${gioResult.stderr}\n${desktop}`);
  const desktopArguments = (await readEventually(argumentLog)).trimEnd().split("\n");
  assert.deepEqual(desktopArguments, ["app", "--open", "--workspace", workspace, "--listen", "127.0.0.1:0"]);
});

test("unrepresentable install roots fail before writes", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "orchard rejected path test "));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });

  const home = path.join(root, "home");
  const sourceBinary = path.join(root, "orchard");
  await mkdir(home, { recursive: true });
  await writeFile(sourceBinary, "#!/bin/sh\nexit 0\n");
  await chmod(sourceBinary, 0o755);

  for (const name of ["data%root", "data\troot", "data\nroot", "data=root", "dáta-root"]) {
    const dataHome = path.join(root, name);
    const result = spawnSync(path.join(packagingDir, "install.sh"), [sourceBinary], {
      env: { ...process.env, HOME: home, XDG_DATA_HOME: dataHome }, encoding: "utf8"
    });
    assert.notEqual(result.status, 0);
    await assert.rejects(access(dataHome), { code: "ENOENT" });
  }
});

test("relative XDG_DATA_HOME uses the same standard default for install and launch", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "orchard relative xdg test "));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });

  const home = path.join(root, "user home");
  const sourceBinary = path.join(root, "orchard");
  const argumentLog = path.join(root, "arguments.log");
  await mkdir(home, { recursive: true });
  await writeFile(sourceBinary, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ORCHARD_ARG_LOG\"\n");
  await chmod(sourceBinary, 0o755);

  const environment = {
    ...process.env,
    HOME: home,
    XDG_DATA_HOME: "relative-data-home",
    ORCHARD_ARG_LOG: argumentLog
  };
  const installResult = spawnSync(path.join(packagingDir, "install.sh"), [sourceBinary], {
    env: environment, encoding: "utf8", cwd: root
  });
  assert.equal(installResult.status, 0, installResult.stderr);

  const defaultDataHome = path.join(home, ".local", "share");
  const launcher = path.join(defaultDataHome, "orchard", "bin", "orchard-workspace");
  await access(launcher);
  await assert.rejects(access(path.join(root, "relative-data-home")), { code: "ENOENT" });
  const launchResult = spawnSync(launcher, [], { env: environment, encoding: "utf8", cwd: root });
  assert.equal(launchResult.status, 0, launchResult.stderr);
  const argumentsSeen = (await readFile(argumentLog, "utf8")).trimEnd().split("\n");
  assert.deepEqual(argumentsSeen, [
    "app", "--open", "--workspace", path.join(defaultDataHome, "orchard", "workspace"), "--listen", "127.0.0.1:0"
  ]);
});

test("documented uninstall ignores a relative XDG_DATA_HOME", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "orchard uninstall docs test "));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });

  const home = path.join(root, "home");
  const defaultDataHome = path.join(home, ".local", "share");
  const relativeDataHome = path.join(root, "relative-data-home");
  const installedFiles = [
    "applications/orchard.desktop",
    "icons/hicolor/scalable/apps/orchard.svg",
    "orchard/bin/orchard-workspace",
    "orchard/bin/orchard"
  ];
  for (const relativePath of installedFiles) {
    await mkdir(path.dirname(path.join(defaultDataHome, relativePath)), { recursive: true });
    await mkdir(path.dirname(path.join(relativeDataHome, relativePath)), { recursive: true });
    await writeFile(path.join(defaultDataHome, relativePath), "installed\n");
    await writeFile(path.join(relativeDataHome, relativePath), "unrelated\n");
  }

  const docs = await readFile(path.resolve(packagingDir, "../docs/app.md"), "utf8");
  const uninstall = docs.match(/## Uninstall[\s\S]*?```sh\n([\s\S]*?)```/);
  assert.ok(uninstall, "missing uninstall shell snippet");
  const uninstallResult = spawnSync("sh", ["-eu", "-c", uninstall[1]], {
    cwd: root,
    env: { ...process.env, HOME: home, XDG_DATA_HOME: "relative-data-home" },
    encoding: "utf8"
  });
  assert.equal(uninstallResult.status, 0, uninstallResult.stderr);

  for (const relativePath of installedFiles) {
    await assert.rejects(access(path.join(defaultDataHome, relativePath)), { code: "ENOENT" });
    assert.equal(await readFile(path.join(relativeDataHome, relativePath), "utf8"), "unrelated\n");
  }
});

test("packaging sources do not embed a developer home path or parse startup output", async () => {
  const launcher = await readFile(path.join(packagingDir, "orchard-workspace"), "utf8");
  const installer = await readFile(path.join(packagingDir, "install.sh"), "utf8");
  assert.doesNotMatch(`${launcher}\n${installer}`, /\/home\/[A-Za-z0-9._-]+/);
  assert.doesNotMatch(launcher, /eval|grep|sed|token=/);
  assert.match(launcher, /exec "\$orchard_binary" app --open --workspace "\$orchard_workspace" --listen 127\.0\.0\.1:0/);
});
