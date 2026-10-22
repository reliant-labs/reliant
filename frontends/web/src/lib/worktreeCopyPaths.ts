/**
 * A new workspace's `copy_files` are EXACT paths relative to the workspace
 * root — the same root the file tree shows. Each names one file or directory
 * that is copied whole; nothing is searched for. `.env` means the `.env` at
 * the root, and `reliant/.env` the one inside the `reliant` repo.
 *
 * The point is to carry over what a fresh checkout does not bring: gitignored
 * pieces like `.env`, local config, or `node_modules`.
 */

/**
 * What the create dialog pre-fills. Exact paths, so these are the `.env` and
 * `.env.local` at the workspace root — copied when present, skipped when not.
 * Nested ones (`api/.env`) must be added by path.
 */
export const DEFAULT_COPY_PATHS: readonly string[] = [".env", ".env.local"];

/** Parse the comma-separated input field into trimmed, non-empty entries. */
export function parseCopyPathsInput(input: string): string[] {
  return input
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0);
}

/**
 * Reject what cannot name a path inside the workspace. Mirrors the server's
 * `copypath.Clean`, which is authoritative; this exists so the mistake shows
 * up in the form instead of as a failed request.
 */
export function copyPathError(entry: string): string | null {
  const slashed = entry.trim().replace(/\\/g, "/");
  if (slashed === "") return "Copy paths cannot be empty.";
  if (slashed.startsWith("/") || /^[A-Za-z]:/.test(slashed)) {
    return `"${entry}" must be relative to the workspace root, not absolute.`;
  }
  const segments: string[] = [];
  for (const part of slashed.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") {
      if (segments.length === 0) return `"${entry}" leaves the workspace.`;
      segments.pop();
      continue;
    }
    segments.push(part);
  }
  if (segments.length === 0) return `"${entry}" names the workspace root itself.`;
  return null;
}

/**
 * Turn a path reported by one repo's `git status` — relative to THAT repo —
 * into a workspace-root path. A single-repo project's repo is the root, so
 * its paths pass through unchanged.
 */
export function repoPathToWorkspacePath(repoRelativePath: string, filePath: string): string {
  const prefix = repoRelativePath.replace(/^\.\/?/, "").replace(/\/+$/, "");
  return prefix === "" ? filePath : `${prefix}/${filePath}`;
}
