// The actual hosted demo declaration, evaluated with the project's pinned forge.
// No deploy, control-plane writes or credentials: `kcl eval` is a local render.
//
// Which forge: FORGE_CMD, the command `task` resolves from forge.yaml's pin
// (Taskfile.yml's FORGE: FORGE_BIN, else `go run …/cmd/forge@<pin>`), which
// `task ci:repo-checks` passes in. A bare `node --test` falls back to the
// forge on PATH. A missing forge fails the test; it is never skipped.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../../", import.meta.url));

const [forge, ...forgeArgs] = (process.env.FORGE_CMD || "forge").trim().split(/\s+/);

// `go run …@<pin>` builds forge on a cold cache: allow for it.
test("demo is exactly one fixtures-only hosted frontend, not the Reliant backend", { timeout: 660_000 }, () => {
  const bundle = JSON.parse(execFileSync(forge, [...forgeArgs, "kcl", "eval", "deploy/kcl/demo", "--format", "json", "-S", "output"], {
    cwd: root,
    timeout: 600_000,
    encoding: "utf8",
    maxBuffer: 4 * 1024 * 1024,
  }));
  assert.equal(bundle.project, "reliant");
  assert.equal(bundle.env, "demo");
  assert.equal(bundle.frontends.length, 1);
  const [site] = bundle.frontends;
  assert.equal(site.name, "forge-demo");
  assert.equal(site.type, "vite");
  assert.equal(site.path, "frontends/web");
  assert.equal(site.source, null, "the demo builds this checkout, not a separate prod pin");
  assert.equal(site.dev_runner, "pnpm");
  assert.deepEqual(site.runtime, { type: "hosted" });
  assert.equal(site.image, "forge-demo");
  assert.equal(site.public_dir, "dist-demo");
  assert.equal(site.asset_dir, "assets");
  assert.deepEqual(site.env_vars.map(({ name, value }) => ({ name, value })), [{ name: "WEB_BUILD_TARGET", value: "demo" }]);
  assert.deepEqual(site.runtime_config, {}, "hosted config stays spec until the platform writes config.js");
  assert.deepEqual(site.runtime_config_spec, {
    DEMO_DATA_SOURCE: { value: "fixtures" },
    DEMO_DATA_URL: { value: "" },
    DEMO_TELEMETRY: { value: "replay" },
    OTEL_ENDPOINT: { value: "/_otel" },
    OTEL_REPLAY: { value: "true" },
  }, "the public config has no auth keys, backend URLs or remote data source");
  assert.equal(bundle.observability.signals.browser, true);
  assert.equal(bundle.observability.signals.replay, true);
  assert.ok(bundle.control_plane, "hosted static publishing needs a control plane");
  for (const field of ["workloads", "clusters", "infra", "databases", "gateways", "http_routes", "grpc_routes", "helm_charts", "manifests", "required_secrets", "rendered_secrets", "control_plane_secrets", "external_secrets_operators"]) {
    assert.deepEqual(bundle[field], [], `${field}: demo must not provision backend/cluster resources or secrets`);
  }
  assert.ok(!bundle.connected_clusters?.length, "no connected cluster");
});
