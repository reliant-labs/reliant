import {
  collapseHomePath,
  splitPathForDisplay as splitDisplayPathAtLastSegment,
} from "@/lib/pathUtils";
import { CLOUD_PROJECT_ROOT } from "@/lib/cloudProjectPath";
import type { Project } from "@/store/projectStore";

/**
 * Pure display helpers for the project picker. Kept out of the components so
 * the table and the page agree on one spelling of "where is this project" and
 * "when was it last used".
 */

export type SortMode = "recent" | "name" | "path";
export type SortDir = "asc" | "desc";

// Each sortable column's default direction. Recency wants newest-first, but
// text columns want A→Z, so the first click on a header shouldn't be a
// uniform "ascending".
export const SORT_DEFAULT_DIR: Record<SortMode, SortDir> = {
  recent: "desc",
  name: "asc",
  path: "asc",
};

// Relative "last active" stamp. Deliberately coarse — in a project picker the
// useful signal is "today vs. last week", not a precise timestamp.
export function formatLastActive(iso: string): string {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "—";
  const mins = Math.floor((Date.now() - then) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  const weeks = Math.floor(days / 7);
  if (weeks < 5) return `${weeks}w ago`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}

/** The exact timestamp, for the hover title on the relative stamp. */
export function formatAbsolute(iso: string): string | undefined {
  const then = new Date(iso);
  return Number.isFinite(then.getTime()) ? then.toLocaleString() : undefined;
}

// Collapse the user's home directory to "~". The daemon may be on any OS, so
// this covers macOS/Linux (/Users/x, /home/x) and Windows (C:\Users\x) alike.
export const displayPath = collapseHomePath;

/**
 * Split a path for middle-ellipsis rendering.
 *
 * CSS alone can only truncate at the end, which hides the leaf directory —
 * exactly the part that distinguishes two checkouts of the same repo. So the
 * head goes to a `truncate` span and the last segment is pinned beside it,
 * giving "~/src/very/long/…/my-project".
 */
export function splitPathForDisplay(path: string): { head: string; tail: string } {
  return splitDisplayPathAtLastSegment(displayPath(path));
}

// Case-insensitive substring match over the fields a user would search by.
// Deliberately not fuzzy: with a handful of projects, substring matching is
// predictable and never surprises you with a "close" hit.
export function projectMatchesQuery(project: Project, needle: string): boolean {
  if (!needle) return true;
  const haystack = `${project.name} ${displayPath(project.path)} ${project.path}`;
  return haystack.toLowerCase().includes(needle);
}

/**
 * Whether a project's checkout lives on a cloud machine.
 *
 * Cloud clones land under CLOUD_PROJECT_ROOT — a contract with the workspace
 * image, not a guess (see lib/cloudProjectPath.ts) — so the path alone
 * answers it without a per-row project_daemons lookup.
 */
export function isCloudProjectPath(path: string): boolean {
  return path === CLOUD_PROJECT_ROOT || path.startsWith(`${CLOUD_PROJECT_ROOT}/`);
}

// reliant.v1.DaemonInfo.daemon_type "managed"/"cloud" is a cloud machine.
// Only cloud machines are clone targets — a self-hosted machine has its own
// filesystem and isn't reachable through the control-plane clone path. The
// set matches normalizeRegisteredDaemonType in reliant's tools_daemon.go.
const CLOUD_DAEMON_TYPES = new Set(["managed", "cloud"]);
export function isCloudDaemon(daemonType: string | undefined): boolean {
  return CLOUD_DAEMON_TYPES.has((daemonType ?? "").toLowerCase());
}

/** Sort `projects` by a column. Comparators are ascending, flipped for desc. */
export function sortProjects(projects: Project[], mode: SortMode, dir: SortDir): Project[] {
  const ascending: Record<SortMode, (a: Project, b: Project) => number> = {
    recent: (a, b) => new Date(a.last_active).getTime() - new Date(b.last_active).getTime(),
    name: (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
    path: (a, b) =>
      displayPath(a.path).localeCompare(displayPath(b.path), undefined, {
        sensitivity: "base",
      }),
  };
  const compare = ascending[mode];
  const sign = dir === "asc" ? 1 : -1;
  return [...projects].sort((a, b) => sign * compare(a, b));
}
