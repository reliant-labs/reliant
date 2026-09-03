import { execSync } from "node:child_process";
import type { Plugin } from "vite";

/**
 * Emits `/version.json` — a STATIC file describing the deployed bundle.
 *
 * ── Why a file and not a route ────────────────────────────────────────
 *
 * The deployed SPA answers every unmatched path with index.html (Firebase
 * Hosting rewrite `** -> /index.html`, see control-plane's
 * deploy/kcl/prod/main.k). So before this existed, `/version`, `/version.json`
 * and `/__version` ALL returned the app shell with HTTP 200 — a request for
 * the version looked like it worked and told you nothing. That is a large part
 * of why the "vmain" stamp survived: there was no way to ask a deployed origin
 * what it was running without opening the UI or exec'ing into a pod.
 *
 * Firebase Hosting serves a real file in the public dir in preference to a
 * rewrite, so emitting this into `dist/` is what takes it out of the SPA
 * fallback's reach. A client-side route could not do that by construction: it
 * would be served BY the fallback, as HTML.
 *
 * ── Where the values come from ────────────────────────────────────────
 *
 * CI env first (the release/deploy workflows know the tag they are building),
 * then git, then "unknown". Never a plausible-looking placeholder: a version
 * field that is wrong is worse than one that admits it does not know, because
 * a bug report then names a build that never shipped.
 */

/** Path of the emitted asset, relative to the bundle root. */
export const VERSION_JSON_FILE = "version.json";

const UNKNOWN = "unknown";

function fromGit(args: string): string {
  try {
    return execSync(`git ${args}`, {
      stdio: ["ignore", "pipe", "ignore"],
      encoding: "utf8",
    }).trim();
  } catch {
    // Not a git checkout (e.g. a source tarball, or a Docker context that
    // excluded .git). Absence is a legitimate answer here, not an error.
    return "";
  }
}

export interface VersionPayload {
  version: string;
  commit: string;
  branch: string;
  built: string;
}

export function resolveVersionPayload(env: NodeJS.ProcessEnv = process.env): VersionPayload {
  // RELEASE_VERSION is what the release workflow already knows; VITE_APP_VERSION
  // is the escape hatch for a build driven by something else.
  const tag = env.RELEASE_VERSION || env.VITE_APP_VERSION || "";
  const version = (tag || fromGit("describe --tags --always --dirty") || UNKNOWN).replace(/^v/, "");

  return {
    version,
    commit: env.GITHUB_SHA || fromGit("rev-parse HEAD") || UNKNOWN,
    branch: env.GITHUB_REF_NAME || fromGit("rev-parse --abbrev-ref HEAD") || UNKNOWN,
    built: new Date().toISOString(),
  };
}

export function versionJson(): Plugin {
  return {
    name: "reliant:version-json",
    // Build only. The dev server has no bundle to describe, and `reliant
    // version` is the answer to the same question locally.
    apply: "build",
    generateBundle() {
      this.emitFile({
        type: "asset",
        fileName: VERSION_JSON_FILE,
        source: JSON.stringify(resolveVersionPayload(), null, 2) + "\n",
      });
    },
  };
}
