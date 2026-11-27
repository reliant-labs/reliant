const assert = require("node:assert/strict");
const { execFileSync } = require("node:child_process");
const path = require("node:path");
const test = require("node:test");

// Exercise the actual KCL with the example's pinned forge, without deploying.
// Run: node --test examples/deployment.test.cjs
function evaluate(example, env) {
  return JSON.parse(
    execFileSync(
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
      { cwd: path.join(__dirname, example), encoding: "utf8" },
    ),
  );
}

for (const env of ["prod", "staging"]) {
  for (const [example, database, command] of [
    ["express-postgres", "express-postgres", ["npm", "run", "migrate"]],
    ["fastapi-postgres", "notes", ["python", "-m", "app.migrate"]],
  ]) {
    test(`${example}/${env}: hosted API and pre-rollout job share managed Postgres`, () => {
      const bundle = evaluate(example, env);
      assert.equal(bundle.secret_provider.type, "hosted");
      assert.equal(bundle.databases.length, 1);
      const db = bundle.databases[0];
      assert.equal(db.name, database);
      assert.equal(db.runtime, "hosted");
      assert.equal(db.spec.engine, "postgres");
      assert.equal(db.spec.deletionPolicy, "retain");
      assert.equal(bundle.workloads.length, 2);
      const api = bundle.workloads.find((w) => w.name === "api");
      const migrate = bundle.workloads.find((w) => w.name === "migrate");
      assert.ok(api);
      assert.ok(migrate);
      assert.equal(api.kind, "service");
      assert.equal(migrate.kind, "job");
      assert.deepEqual(migrate.spec.command, command);
      assert.deepEqual(migrate.spec.before, ["*"]);
      assert.equal(migrate.image, api.image);
      assert.deepEqual(migrate.build, api.build);
      for (const workload of [api, migrate]) {
        assert.equal(workload.runtime.type, "hosted");
        assert.deepEqual(
          workload.spec.env.filter((entry) => entry.name === "DATABASE_URL"),
          [{ name: "DATABASE_URL", databaseRef: { name: database } }],
        );
        if (env === "prod") {
          assert.deepEqual(workload.spec.resources, {
            cpuRequestMillicores: 125,
            cpuLimitMillicores: 125,
            memoryRequestBytes: 128 * 1024 * 1024,
            memoryLimitBytes: 128 * 1024 * 1024,
          });
        }
      }
      assert.equal(bundle.control_plane.claimable ?? false, env === "prod");
    });
  }

  test(`vite-spa/${env}: static export uses hosted hosting without backend infrastructure`, () => {
    const bundle = evaluate("vite-spa", env);
    assert.deepEqual(bundle.workloads, []);
    assert.deepEqual(bundle.databases, []);
    assert.equal(bundle.frontends.length, 1);
    const web = bundle.frontends[0];
    assert.equal(web.name, "web");
    assert.equal(web.type, "vite");
    assert.equal(web.path, ".");
    assert.equal(web.public_dir, "dist");
    assert.equal(web.runtime.type, "hosted");
    assert.equal(web.image, "web");
  });
}
