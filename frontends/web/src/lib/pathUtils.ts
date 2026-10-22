/**
 * Separator-aware filesystem path helpers.
 *
 * The daemon that owns a path may be running on a different OS than the
 * browser rendering it — a Windows daemon reached from a Mac, or a Linux
 * cloud workspace reached from Windows. So none of these helpers may assume
 * the local platform's convention, and none of them may *rewrite* a path into
 * a different convention: whatever the daemon handed us has to round-trip back
 * to it byte-for-byte. Every function here preserves the separator style of
 * its input.
 *
 * Scope: real filesystem paths only. URLs and react-router routes are always
 * "/" and must keep using plain string operations.
 */

/** Windows drive-qualified absolute path: `C:\...` or `C:/...`. */
const WINDOWS_DRIVE_ABSOLUTE = /^[A-Za-z]:[\\/]/;

/** Just the drive designator, e.g. the `C:` of `C:\Users`. */
const WINDOWS_DRIVE_PREFIX = /^[A-Za-z]:/;

/** UNC share root: `\\server\share`, optionally followed by more path. */
const UNC_ROOT = /^\\\\[^\\/]+\\[^\\/]+/;

/**
 * True when `path` uses Windows conventions — a drive letter or a UNC share.
 *
 * A path containing only backslashes but no drive/UNC prefix (e.g. `foo\bar`)
 * is deliberately NOT Windows here: it is relative, and the callers that care
 * about separator style are all working from an absolute daemon-reported path.
 */
export function isWindowsPath(path: string): boolean {
  return WINDOWS_DRIVE_ABSOLUTE.test(path) || UNC_ROOT.test(path) || WINDOWS_DRIVE_PREFIX.test(path);
}

/**
 * True for POSIX-absolute (`/...`), drive-absolute (`C:\...`, `C:/...`) and
 * UNC (`\\server\share`) paths.
 *
 * A bare `C:` is excluded: on Windows that is drive-*relative* (it means "the
 * current directory on C:"), not a root.
 */
export function isAbsolutePath(path: string): boolean {
  if (!path) return false;
  return path.startsWith("/") || WINDOWS_DRIVE_ABSOLUTE.test(path) || UNC_ROOT.test(path);
}

/** The separator `path` is written with — `\` for Windows-style, else `/`. */
export function pathSeparator(path: string): string {
  if (UNC_ROOT.test(path)) return "\\";
  if (WINDOWS_DRIVE_ABSOLUTE.test(path)) return path[2] === "\\" ? "\\" : "/";
  if (WINDOWS_DRIVE_PREFIX.test(path) && path.includes("\\")) return "\\";
  return "/";
}

/**
 * The topmost navigable directory of `path`, with its trailing separator.
 *
 * POSIX has one root, `/`. Windows has one root *per volume*, so `C:\a\b`
 * bottoms out at `C:\` and `\\srv\share\a` at `\\srv\share\`. Returns `""`
 * for a relative path, which has no root to navigate to.
 */
export function pathRoot(path: string): string {
  const unc = UNC_ROOT.exec(path);
  if (unc) return `${unc[0]}\\`;
  if (WINDOWS_DRIVE_ABSOLUTE.test(path)) return path.slice(0, 3);
  if (path.startsWith("/")) return "/";
  return "";
}

/**
 * True when `path` is its own root (`/`, `C:\`, `\\srv\share`).
 *
 * Compared with trailing separators removed from both sides, because a UNC
 * share is a root whether or not it was written with one.
 */
export function isPathRoot(path: string): boolean {
  const root = pathRoot(path);
  return root !== "" && trimTrailingSeparators(path) === trimTrailingSeparators(root);
}

/** Unconditionally drop trailing separators (`/` becomes `""`). */
function trimTrailingSeparators(path: string): string {
  return path.replace(/[\\/]+$/, "");
}

/** Drop trailing separators, but never the ones that constitute a root. */
function stripTrailingSeparators(path: string): string {
  if (isPathRoot(path)) return pathRoot(path);
  return trimTrailingSeparators(path) || path;
}

/**
 * The last segment of `path` — the folder or file name.
 *
 * Trailing separators are ignored, so `C:\a\b\` and `C:\a\b` both yield `b`.
 * A root has no basename and returns `""`.
 */
export function basename(path: string): string {
  if (!path) return "";
  const trimmed = stripTrailingSeparators(path);
  if (isPathRoot(trimmed)) return "";
  const lastSeparator = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  if (lastSeparator < 0) {
    // Drive-relative like `C:foo` — the drive prefix is not part of the name.
    return trimmed.replace(WINDOWS_DRIVE_PREFIX, "");
  }
  return trimmed.slice(lastSeparator + 1);
}

/**
 * The parent directory of `path`, in `path`'s own separator style.
 *
 * A root is its own parent, which is what lets an "up" control clamp instead
 * of walking off the top of the volume.
 */
export function dirname(path: string): string {
  if (!path) return "";
  const trimmed = stripTrailingSeparators(path);
  const root = pathRoot(trimmed);
  if (isPathRoot(trimmed)) return trimmed;
  const lastSeparator = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  if (lastSeparator < 0) return "";
  const parent = trimmed.slice(0, lastSeparator);
  // Slicing off the last separator can eat the root's own separator
  // (`/foo` -> `` , `C:\foo` -> `C:`); restore it.
  if (root && parent.length < root.length) return root;
  return parent;
}

/**
 * Append `segment` to `parent`, using `parent`'s separator style.
 *
 * `parent` may already end in a separator (a root always does), and the result
 * never doubles it.
 */
export function joinPath(parent: string, segment: string): string {
  if (!parent) return segment;
  if (!segment) return parent;
  const separator = pathSeparator(parent);
  const base = parent.replace(/[\\/]+$/, "");
  // Trimming a root leaves the bare designator (`` for `/`, `C:` for `C:\`),
  // so re-add the separator explicitly rather than relying on what's left.
  if (base.length < pathRoot(parent).length || base === "") {
    return `${pathRoot(parent)}${segment}`;
  }
  return `${base}${separator}${segment}`;
}

export interface PathCrumb {
  /** The text to show for this level. */
  name: string;
  /** The full path this level navigates to. */
  path: string;
}

/**
 * `path` broken into breadcrumb levels, root first.
 *
 * The root crumb is labelled with the root itself — `/` on POSIX, `C:\` or
 * `\\srv\share\` on Windows — because there is no universal "top" to point at
 * on a Windows volume. Returns `[]` for an empty or relative path.
 */
export function pathCrumbs(path: string): PathCrumb[] {
  if (!isAbsolutePath(path)) return [];
  const root = pathRoot(path);
  const crumbs: PathCrumb[] = [{ name: root, path: root }];
  const remainder = stripTrailingSeparators(path).slice(root.length);
  const segments = remainder.split(/[\\/]/).filter(Boolean);
  let current = root;
  for (const segment of segments) {
    current = joinPath(current, segment);
    crumbs.push({ name: segment, path: current });
  }
  return crumbs;
}

/** Home-directory layouts the daemon can report, per platform. */
const HOME_PREFIXES = [
  // macOS and Linux.
  /^\/(?:Users|home)\/[^/]+/,
  // Windows Vista+ (`C:\Users\sean`) and the legacy XP layout some machines
  // still carry (`C:\Documents and Settings\sean`).
  /^[A-Za-z]:[\\/]Users[\\/][^\\/]+/i,
  /^[A-Za-z]:[\\/]Documents and Settings[\\/][^\\/]+/i,
];

/**
 * Collapse a user's home directory to `~` for display.
 *
 * Display only — never feed the result back to the daemon.
 */
export function collapseHomePath(path: string): string {
  for (const prefix of HOME_PREFIXES) {
    if (prefix.test(path)) return path.replace(prefix, "~");
  }
  return path;
}

/**
 * Split a path just before its last segment, for middle-ellipsis rendering.
 *
 * `head` keeps the trailing separator so the two halves concatenate back to
 * the original.
 */
export function splitPathForDisplay(path: string): { head: string; tail: string } {
  const lastSeparator = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  if (lastSeparator <= 0) return { head: "", tail: path };
  return { head: path.slice(0, lastSeparator + 1), tail: path.slice(lastSeparator + 1) };
}

/** True when `name` contains a separator, i.e. is not a single path segment. */
export function containsSeparator(name: string): boolean {
  return name.includes("/") || name.includes("\\");
}

/**
 * An example absolute path to show the user, matching their own platform.
 *
 * Used in placeholder text and validation errors: a Windows user told to enter
 * a path "like /Users/you/projects/app" has been given an impossible example.
 */
export function examplePathForPlatform(os: string): string {
  return os === "windows"
    ? "C:\\Users\\you\\projects\\my-app"
    : "/Users/you/projects/my-app";
}
