import assert from "node:assert/strict";
import { chmod, mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

const packagingDir = path.resolve(new URL("..", import.meta.url).pathname);

test("installer and launcher preserve files and quote workspace paths as one argument", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "orchard package test "));
  t.after(async () => {
    const { rm } = await import("node:fs/promises");
    await rm(root, { recursive: true, force: true });
  });

  const home = path.join(root, "user home");
  const dataHome = path.join(root, "xdg data & spaces");
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

  const desktop = await readFile(path.join(dataHome, "applications", "orchard.desktop"), "utf8");
  assert.match(desktop, new RegExp(`Exec="${path.join(dataHome, "orchard", "bin", "orchard-workspace").replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}"`));

  const workspace = path.join(root, "Projects with spaces");
  const launchResult = spawnSync(path.join(dataHome, "orchard", "bin", "orchard-workspace"), [], {
    env: { ...environment, ORCHARD_WORKSPACE: workspace }, encoding: "utf8"
  });
  assert.equal(launchResult.status, 0, launchResult.stderr);
  const argumentsSeen = (await readFile(argumentLog, "utf8")).trimEnd().split("\n");
  assert.deepEqual(argumentsSeen, ["app", "--open", "--workspace", workspace, "--listen", "127.0.0.1:0"]);
});

test("packaging sources do not embed a developer home path or parse startup output", async () => {
  const launcher = await readFile(path.join(packagingDir, "orchard-workspace"), "utf8");
  const installer = await readFile(path.join(packagingDir, "install.sh"), "utf8");
  assert.doesNotMatch(`${launcher}\n${installer}`, /\/home\/[A-Za-z0-9._-]+/);
  assert.doesNotMatch(launcher, /eval|grep|sed|token=/);
  assert.match(launcher, /exec "\$orchard_binary" app --open --workspace "\$orchard_workspace" --listen 127\.0\.0\.1:0/);
});
