// Render the real release binding with the pinned forge, not a copied fixture.
// Picked up by task ci:repo-checks alongside forge-demo-env.test.mjs.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import test from "node:test";

const root = fileURLToPath(new URL("../../", import.meta.url));
const [forge, ...forgeArgs] = (process.env.FORGE_CMD || `${root}scripts/forge.sh`).trim().split(/\s+/);

function render(env, options = []) {
  return JSON.parse(execFileSync(forge, [...forgeArgs, "kcl", "eval", `deploy/kcl/${env}`, "--format", "json", "-S", "output", ...options], {
    cwd: root,
    timeout: 600_000,
    encoding: "utf8",
    maxBuffer: 4 * 1024 * 1024,
  }));
}

// Distinct non-default values catch swapped option/variable mappings as well
// as omitted stamps. No binaries are built and no credentials are needed.
test("release binds the CLI GoBuild with five unique stamped platform outputs", { timeout: 660_000 }, () => {
  const stamps = [
    ["stamp_version", "Version", "1.9.0-rc.7"],
    ["stamp_commit", "Commit", "0123456789abcdef0123456789abcdef01234567"],
    ["stamp_date", "Date", "2026-11-19T16:00:00Z"],
    ["stamp_branch", "Branch", "release/cli-contract"],
    ["stamp_commit_time", "CommitTime", "1795103999"],
  ];
  const bundle = render("release", stamps.flatMap(([option, , value]) => ["-D", `${option}=${value}`]));
  assert.equal(bundle.project, "reliant");
  assert.equal(bundle.env, "release");
  const clis = bundle.workloads.filter(({ name }) => name === "reliant-cli");
  assert.equal(clis.length, 1, "the release must bind exactly one CLI workload");
  const [cli] = clis;
  assert.equal(cli.kind, "tool");
  assert.equal(cli.build.type, "go");
  assert.equal(cli.build.cmd, "./cmd/reliant");
  assert.equal(cli.runtime.type, "build-only");
  const variants = cli.runtime.build_variants;
  const expected = [
    ["linux", "amd64", "bin/reliant-cli-linux-amd64"],
    ["linux", "arm64", "bin/reliant-cli-linux-arm64"],
    ["darwin", "amd64", "bin/reliant-cli-darwin-amd64"],
    ["darwin", "arm64", "bin/reliant-cli-darwin-arm64"],
    ["windows", "amd64", "bin/reliant-cli-windows-amd64.exe"],
  ];
  // forge's buildVariant names outputs <workload>-<variant>, appending .exe
  // for Windows (internal/cli/build.go:3268-3278 at the project's forge pin).
  const outputs = variants.map((variant) => {
    const name = variant.output_name || `${cli.name}-${variant.name}`;
    return [variant.goos, variant.goarch, `bin/${name}${variant.goos === "windows" && !name.endsWith(".exe") ? ".exe" : ""}`];
  });
  assert.deepEqual(outputs.sort(), expected.sort());
  assert.equal(new Set(outputs.map(([, , output]) => output)).size, 5);
  const ldflags = ["-s", "-w", ...stamps.map(([, variable, value]) => `-X github.com/reliant-labs/reliant/internal/version.${variable}=${value}`)];
  for (const variant of variants) {
    assert.deepEqual([...variant.ldflags].sort(), [...ldflags].sort(), `${variant.name}: all five supplied stamps must reach the linker`);
  }
});

// Discover the actual environment entrypoints so a future deploy environment
// cannot accidentally inherit this release-only artifact without failing here.
for (const entry of readdirSync(`${root}deploy/kcl`, { withFileTypes: true })) {
  if (!entry.isDirectory() || entry.name === "release" || !existsSync(`${root}deploy/kcl/${entry.name}/main.k`)) continue;
  test(`${entry.name} does not bind the release-only CLI`, { timeout: 660_000 }, () => {
    const bundle = render(entry.name);
    assert.ok(!bundle.workloads.some(({ name }) => name === "reliant-cli"), "CLI must not enter a deployed environment bundle");
  });
}
