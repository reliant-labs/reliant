// The actual hosted demo declaration, evaluated with the project's pinned forge.
// No deploy, control-plane writes or credentials: `kcl eval` is a local render.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../../", import.meta.url));

test("demo is exactly one fixtures-only hosted frontend, not the Reliant backend", { timeout: 180_000 }, () => {
  const bundle = JSON.parse(execFileSync("forge", ["kcl", "eval", "deploy/kcl/demo", "--format", "json", "-S", "output"], {
    cwd: root,
    timeout: 150_000,
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
