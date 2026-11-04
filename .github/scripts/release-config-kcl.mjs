// Renders the packaged desktop app's release config out of control-plane's
// KCL. The ONE implementation of that render: sync-release-config.mjs (the
// generator and drift gate) and electron/scripts/with-release-config.mjs (the
// build wrapper) both call it, so they cannot disagree about how the KCL is
// evaluated.
//
// ## Why `kcl run` alone cannot do it
//
// control-plane's `deploy/kcl/desktop_release.k` reaches `import forge`, and a
// forge project declares no `forge` dependency in kcl.mod: the module is
// EMBEDDED IN THE FORGE BINARY and supplied to every evaluation as an external
// package (forge ADR 0003 — "the stock `kcl` CLI cannot render a project on its
// own"). A bare `kcl run` fails with `CannotFindModule: forge` on a clean
// machine, and on a laptop it may silently resolve a stale global
// `~/.kcl/kpm/forge_kcl` left by an older forge — rendering a DIFFERENT module
// than control-plane deploys with, while still reporting success. The wrapper
// kept a bare `kcl run` after the gate moved off it, which is how its tests
// went red the moment that stale global copy was gone.
//
// ## How it renders
//
// 1. `forge kcl eval` evaluates the entrypoint with control-plane's pinned
//    forge, which materializes forge's KCL module into FORGE_KCL_MODULE_CACHE.
//    Pointing that at a scratch dir means the module used is exactly the one
//    this forge supplies, never whatever happens to be cached already.
// 2. `kcl run -E forge=<that module>` renders the same file again for the
//    output. `forge kcl eval` sorts object keys, and the committed
//    release.config.json keeps the KCL's declaration order — the drift gate
//    compares it byte for byte.

import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export const KCL_ENTRYPOINT = "deploy/kcl/desktop_release.k";

/** The KCL is present but does not render: a real defect, never a reason to skip. */
export class ReleaseConfigRenderError extends Error {}

function run(command, args, options) {
  return execFileSync(command, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], ...options });
}

/**
 * Render `release_config` for `env` from the control-plane checkout at
 * `controlPlaneDir`.
 *
 * Returns `{ config }`, or `{ unavailable }` naming why the KCL cannot be
 * reached at all (no checkout, no forge, no kcl) — the fork case, where the
 * committed file is legitimately authoritative. Throws
 * ReleaseConfigRenderError when the KCL is reachable and fails to render.
 */
export function renderReleaseConfig(controlPlaneDir, env) {
  if (!existsSync(join(controlPlaneDir, KCL_ENTRYPOINT))) {
    return { unavailable: `no control-plane checkout at ${controlPlaneDir}` };
  }

  const scratch = mkdtempSync(join(tmpdir(), "release-config-"));
  const moduleCache = join(scratch, "forge-kcl");
  try {
    try {
      run("forge", ["kcl", "eval", KCL_ENTRYPOINT, "-D", `env=${env}`, "-S", "release_config"], {
        cwd: controlPlaneDir,
        env: { ...process.env, FORGE_KCL_MODULE_CACHE: moduleCache, FORGE_SILENCE_EXPERIMENTAL: "1" },
      });
    } catch (err) {
      if (err.code === "ENOENT") return { unavailable: "forge is not installed" };
      throw new ReleaseConfigRenderError(`forge kcl eval failed for env=${env}:\n${err.stderr || err.message}`);
    }

    // The module lands in <cache>/<content-hash>/, with `.complete` written last.
    const forgeModule = existsSync(moduleCache)
      ? readdirSync(moduleCache, { withFileTypes: true })
          .filter((e) => e.isDirectory() && existsSync(join(moduleCache, e.name, ".complete")))
          .map((e) => join(moduleCache, e.name))[0]
      : undefined;
    if (!forgeModule) {
      throw new ReleaseConfigRenderError(`forge rendered the KCL but left no complete KCL module in ${moduleCache}`);
    }

    // The JSON goes to a file, not stdout: kcl prints "waiting for package-cache
    // lock..." to stdout while another kcl holds the shared package cache.
    const out = join(scratch, "release_config.json");
    try {
      run(
        "kcl",
        ["run", KCL_ENTRYPOINT, "-E", `forge=${forgeModule}`, "-D", `env=${env}`, "-S", "release_config", "--format", "json", "-o", out],
        { cwd: controlPlaneDir },
      );
    } catch (err) {
      if (err.code === "ENOENT") return { unavailable: "kcl is not installed" };
      throw new ReleaseConfigRenderError(`kcl render failed for env=${env}:\n${err.stderr || err.message}`);
    }
    return { config: JSON.parse(readFileSync(out, "utf8")) };
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}
