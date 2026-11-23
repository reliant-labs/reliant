// Renders the packaged desktop app's release config out of control-plane's
// KCL. The ONE implementation of that render: sync-release-config.mjs (the
// generator and drift gate) and electron/scripts/with-release-config.mjs (the
// build wrapper) both call it, so they cannot disagree about how the KCL is
// evaluated.
//
// ## Why `kcl run` cannot do it
//
// control-plane's `deploy/kcl/desktop_release.k` reaches `import forge`, and a
// forge project declares no `forge` dependency in kcl.mod: the module is
// EMBEDDED IN THE FORGE BINARY and supplied to every evaluation as an external
// package (forge ADR 0003 — "the stock `kcl` CLI cannot render a project on its
// own"). A bare `kcl run` fails with `CannotFindModule: forge` on a clean
// machine, and on a laptop it may silently resolve a stale global
// `~/.kcl/kpm/forge_kcl` left by an older forge — rendering a DIFFERENT module
// than control-plane deploys with, while still reporting success.
//
// Not even `kcl run -E forge=<the module forge materialized>` can: forge's
// module imports `kcl_plugin.forge` (telemetry.k), a KCL plugin the forge
// binary registers in its own evaluator, which the stock `kcl` CLI does not
// have ("the plugin package `kcl_plugin.forge` is not found, please confirm
// if plugin mode is enabled"). Only a forge evaluation renders it.
//
// ## How it renders
//
// `forge env kcl eval --format json` (or `reliant forge env kcl eval`, when no
// forge is on PATH), run in the control-plane checkout, so either hands the
// command to the forge control-plane pins. `--format json` prints the document
// the way `kcl run --format json` does — keys in the order the KCL declares
// them, never alphabetised — so the committed release.config.json, which
// keeps that order, still compares byte for byte. FORGE_KCL_MODULE_CACHE points
// at a scratch dir, so the module used is exactly the one this forge supplies,
// never whatever happens to be cached already.

import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
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
 * reached at all (no checkout, no forge) — the fork case, where the committed
 * file is legitimately authoritative. Throws ReleaseConfigRenderError when the
 * KCL is reachable and fails to render.
 */
export function renderReleaseConfig(controlPlaneDir, env) {
  if (!existsSync(join(controlPlaneDir, KCL_ENTRYPOINT))) {
    return { unavailable: `no control-plane checkout at ${controlPlaneDir}` };
  }

  const scratch = mkdtempSync(join(tmpdir(), "release-config-"));
  const moduleCache = join(scratch, "forge-kcl");
  try {
    // forge on PATH, else `reliant forge`: run in the checkout, either hands
    // the command to the forge control-plane pins.
    for (const [command, ...route] of [["forge"], ["reliant", "forge"]]) {
      let out;
      try {
        out = run(
          command,
          [...route, "env", "kcl", "eval", KCL_ENTRYPOINT, "-D", `env=${env}`, "-S", "release_config", "--format", "json"],
          {
            cwd: controlPlaneDir,
            env: { ...process.env, FORGE_KCL_MODULE_CACHE: moduleCache, FORGE_SILENCE_EXPERIMENTAL: "1" },
          },
        );
      } catch (err) {
        if (err.code === "ENOENT") continue;
        throw new ReleaseConfigRenderError(`forge env kcl eval failed for env=${env}:\n${err.stderr || err.message}`);
      }
      try {
        return { config: JSON.parse(out) };
      } catch (err) {
        throw new ReleaseConfigRenderError(
          `forge env kcl eval --format json printed no JSON for env=${env} (${err.message}):\n${out}`,
        );
      }
    }
    return { unavailable: "neither forge nor reliant is installed" };
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}
