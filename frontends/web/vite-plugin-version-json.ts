import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

import { UNKNOWN, type BuildInfo } from "./src/lib/releaseIdentity";

/**
 * Bakes THIS bundle's identity into the app and emits it as `/version.json`.
 *
 * ── What it fixes ─────────────────────────────────────────────────────
 *
 * The About page used to show only the SERVER's Version RPC, and the bundle
 * knew nothing about itself. Prod's reliant image was built without -ldflags,
 * so the RPC answered "unknown" and the page read "version unknown" — while
 * the build that produced the page knew perfectly well what it was. The same
 * blindness filed every web error report under `reliant@unknown`.
 *
 * So the bundle now carries its own identity, `__RELIANT_BUILD__` (read through
 * src/lib/appBuild.ts), and the About page shows it beside the server's.
 *
 * ── Where the values come from ────────────────────────────────────────
 *
 * The version is scripts/release-identity.sh's answer — the SAME resolver the
 * Go binary's stamp comes from, so "app" and "server" can only differ when
 * they really are different builds. It is fed what the build environment
 * knows that the checkout cannot:
 *
 *   FORGE_SOURCE_REF / FORGE_SOURCE_COMMIT   forge's GitSource pin. forge fetches
 *                                            it shallow with no tags, so a
 *                                            tag-pinned build learns its tag here.
 *   FORGE_RELEASE                            the forge release shipping the bundle.
 *   RELEASE_VERSION                          an explicit tag, for a build driven
 *                                            by something else.
 *
 * VITE_APP_VERSION is deliberately NOT read here. forge's Firebase deploy
 * fills it with its frontend build id (a content hash, e.g. 657f69e51d5e) so
 * telemetry's service.version matches the uploaded source maps. Reading it as
 * a tag made release-identity.sh refuse every forge-driven prod build.
 *
 * GITHUB_SHA is deliberately NOT read: control-plane's CI builds this bundle
 * from a sibling checkout, where GITHUB_SHA names control-plane's commit.
 * `git` in the checkout being built is always right about that checkout.
 *
 * ── The guard ─────────────────────────────────────────────────────────
 *
 * A production build that cannot name itself FAILS. That is every shipped
 * build: forge's Firebase deploy and release.yml's desktop renderer both run
 * `vite build`. "unknown" in a shipped bundle is the bug, not a fallback.
 * RELIANT_ALLOW_UNVERSIONED_BUILD=1 opts out, for a build from a source
 * tarball that genuinely has no history.
 *
 * ── Why a file as well ────────────────────────────────────────────────
 *
 * The deployed SPA answers unmatched paths with index.html, so a client-side
 * `/version` route would be served BY the fallback, as HTML. A real file in
 * the public dir is served in preference to it, which makes `curl
 * https://app.reliantlabs.io/version.json` an honest answer to "what is
 * deployed?" without opening the UI.
 */

/** Path of the emitted asset, relative to the bundle root. */
export const VERSION_JSON_FILE = "version.json";

/** The reliant checkout this frontends/web/ directory belongs to. */
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

/** The one identity resolver (see its header). */
export const RELEASE_IDENTITY_SCRIPT = path.join(REPO_ROOT, "scripts", "release-identity.sh");

/** Escape hatch for a production build with no history to read. */
export const ALLOW_UNVERSIONED_ENV = "RELIANT_ALLOW_UNVERSIONED_BUILD";

/** Seams, so the resolution is testable without the real checkout. */
export interface ResolveDeps {
  /** Runs release-identity.sh; returns its stdout, throws on failure. */
  identity: (args: string[]) => string;
  /** Runs `git <args>` in the checkout; returns stdout, or "" on failure. */
  git: (args: string[]) => string;
  now: () => Date;
}

const defaultDeps: ResolveDeps = {
  identity: (args) =>
    execFileSync("bash", [RELEASE_IDENTITY_SCRIPT, ...args], {
      stdio: ["ignore", "pipe", "pipe"],
      encoding: "utf8",
    }).trim(),
  git: (args) => {
    try {
      return execFileSync("git", ["-C", REPO_ROOT, ...args], {
        stdio: ["ignore", "pipe", "ignore"],
        encoding: "utf8",
      }).trim();
    } catch {
      // Not a git checkout (a source tarball). Absence is a legitimate answer.
      return "";
    }
  },
  now: () => new Date(),
};

export interface ResolvedBuild {
  info: BuildInfo;
  /** Why the version is unknown, when it is — the resolver's own words. */
  problem?: string;
}

export function resolveBuild(
  env: NodeJS.ProcessEnv = process.env,
  deps: ResolveDeps = defaultDeps,
): ResolvedBuild {
  const commit = env.FORGE_SOURCE_COMMIT || deps.git(["rev-parse", "HEAD"]);

  const args = ["--repo", REPO_ROOT];
  // VITE_APP_VERSION is NOT a tag source: forge injects its frontend build id
  // (a content hash) there, for telemetry's service.version (src/lib/telemetry.ts).
  const tag = env.RELEASE_VERSION;
  if (tag) args.push("--tag", tag);
  if (env.FORGE_SOURCE_REF) args.push("--ref", env.FORGE_SOURCE_REF);
  // Only a commit the ENVIRONMENT named is passed on: naming one skips the
  // resolver's dirty check, which is right for forge's pin (its build may
  // touch the lockfile) and wrong for a developer's working tree.
  if (env.FORGE_SOURCE_COMMIT) args.push("--commit", env.FORGE_SOURCE_COMMIT);

  let version = UNKNOWN;
  let problem: string | undefined;
  try {
    version = deps.identity(args) || UNKNOWN;
  } catch (err) {
    const stderr = (err as { stderr?: unknown }).stderr;
    problem = (typeof stderr === "string" && stderr.trim()) || String(err);
  }

  const branch = deps.git(["rev-parse", "--abbrev-ref", "HEAD"]);
  const info: BuildInfo = {
    version,
    commit: commit || UNKNOWN,
    // A detached checkout (every forge and CI build) reports "HEAD" — not a ref.
    ref: env.FORGE_SOURCE_REF || (branch && branch !== "HEAD" ? branch : UNKNOWN),
    built: deps.now().toISOString(),
  };
  if (env.FORGE_RELEASE) info.release = env.FORGE_RELEASE;
  return { info, problem };
}

/**
 * The refusal a shipped build gets when it cannot name itself, or null when
 * the build may proceed.
 */
export function unversionedBuildError(
  resolved: ResolvedBuild,
  env: NodeJS.ProcessEnv = process.env,
): string | null {
  if (resolved.info.version !== UNKNOWN || env[ALLOW_UNVERSIONED_ENV] === "1") return null;
  return [
    "reliant: refusing a production build that cannot name its own version.",
    `  ${resolved.problem ?? "scripts/release-identity.sh returned nothing"}`,
    "  A shipped bundle reporting \"unknown\" is the bug this guard exists for (the About page read",
    "  \"version unknown\" in prod). Build from a git checkout, or pass the identity in: RELEASE_VERSION=<tag>,",
    "  or forge's FORGE_SOURCE_COMMIT/FORGE_SOURCE_REF.",
    `  A build that genuinely has no history (a source tarball) may set ${ALLOW_UNVERSIONED_ENV}=1.`,
  ].join("\n");
}

export function versionJson(
  env: NodeJS.ProcessEnv = process.env,
  deps: ResolveDeps = defaultDeps,
): Plugin {
  let resolved: ResolvedBuild | undefined;
  return {
    name: "reliant:version-json",
    // Everywhere but vitest: the dev server shows the working tree's identity
    // too, and tests must not depend on the checkout they run in (appBuild()
    // reports null there instead).
    apply: (_config, { mode }) => mode !== "test",
    config(_config, { command, mode }) {
      resolved = resolveBuild(env, deps);
      if (command === "build" && mode === "production") {
        const refusal = unversionedBuildError(resolved, env);
        if (refusal) throw new Error(refusal);
      }
      return { define: { __RELIANT_BUILD__: JSON.stringify(resolved.info) } };
    },
    generateBundle() {
      if (!resolved) return;
      this.emitFile({
        type: "asset",
        fileName: VERSION_JSON_FILE,
        source: JSON.stringify(resolved.info, null, 2) + "\n",
      });
    },
  };
}
