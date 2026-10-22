/**
 * Release identity — what "which build is this?" looks like to a person.
 *
 * Two builds answer that question on the About page: THIS bundle (baked in at
 * build time, see vite-plugin-version-json.ts and appBuild.ts) and the server
 * it talks to (the Version RPC, stamped into the Go binary with -ldflags). Both
 * are derived by the same resolver, scripts/release-identity.sh:
 *
 *   1.8.1                 built at the v1.8.1 tag
 *   1.8.2-dev+8bcfc6ee    built at commit 8bcfc6ee, after release 1.8.1
 *   8bcfc6ee              a commit, with no release to place it after
 *
 * This module only turns such a stamp into display text. It has to tolerate
 * every stamp a deployed binary may still carry — release.yml's bare "1.8.1",
 * a `git describe` string, a bare sha, the literal "unknown" — because an old
 * server outlives the change that fixed its stamp.
 *
 * Pure: no globals, no I/O. vite-plugin-version-json.ts imports BuildInfo
 * from here, so this file must type-check under the node tsconfig too.
 */

/** The value every unstamped field holds — Go's internal/version default too. */
export const UNKNOWN = "unknown";

/**
 * What a build knows about itself. Emitted as /version.json and baked into
 * the bundle as `__RELIANT_BUILD__`.
 */
export interface BuildInfo {
  /** Release identity from scripts/release-identity.sh, or "unknown". */
  version: string;
  /** Full commit sha the bundle was built from, or "unknown". */
  commit: string;
  /** What was built: forge's GitSource pin, else the branch, else "unknown". */
  ref: string;
  /** The forge release that shipped this bundle; absent outside one. */
  release?: string;
  /** ISO-8601 build time. */
  built: string;
}

// Values that mean "nobody stamped this". "(devel)" is what Go's build info
// reports for a binary built inside its own module.
const NOT_A_VERSION = new Set(["", UNKNOWN, "(devel)", "dev"]);

const SEMVER = /^v?\d+\.\d+\.\d+/;
const SHA = /^([0-9a-f]{7,40})(\.dirty)?$/i;

/** The 8-character abbreviation release identities use. */
export function shortSha(sha: string): string {
  return sha.slice(0, 8).toLowerCase();
}

function isKnown(value: string | null | undefined): value is string {
  return value != null && !NOT_A_VERSION.has(value.trim().toLowerCase());
}

/**
 * Display text for a version stamp, or null when it names no build.
 *
 * `commit` is the fallback: a binary with a commit but no version stamp is
 * still identifiable, and a short sha is far more useful in a bug report than
 * "unknown".
 */
export function formatVersion(
  version: string | null | undefined,
  commit?: string | null,
): string | null {
  if (isKnown(version)) {
    const v = version.trim();
    if (SEMVER.test(v)) return `v${v.replace(/^v/, "")}`;
    const sha = SHA.exec(v);
    if (sha) return shortSha(sha[1]) + (sha[2] ?? "");
    return v;
  }
  if (isKnown(commit) && SHA.test(commit.trim())) return shortSha(commit.trim());
  return null;
}
