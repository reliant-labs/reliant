import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

// Evaluate the actual environment, not a duplicate workload fixture. The forge
// wrapper honors forge.yaml's pin; no deploy, credentials or live gateway needed.
for (const env of ["prod", "staging"]) {
  test(`${env} binds the caller token to the hosted API`, () => {
    const output = execFileSync(
      "reliant",
      [
        "forge",
        "env",
        "kcl",
        "eval",
        `deploy/kcl/${env}/main.k`,
        "-S",
        "output",
        "--format",
        "json",
      ],
      { cwd: fileURLToPath(new URL(".", import.meta.url)), encoding: "utf8" },
    );
    const bundle = JSON.parse(output);
    assert.equal(bundle.secret_provider.type, "hosted");
    const api = bundle.workloads.find((workload) => workload.name === "api");
    assert.ok(api, "environment must include the API workload");
    assert.equal(api.runtime.type, "hosted");
    const tokens = api.spec.env.filter(
      (entry) => entry.name === "APP_API_TOKEN",
    );
    assert.deepEqual(tokens, [
      {
        name: "APP_API_TOKEN",
        managedSecret: { name: "APP_API_TOKEN", optional: true },
      },
    ]);
  });
}
