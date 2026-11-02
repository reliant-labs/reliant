/**
 * What a tool row says happened, in words: "Edited totals.ts", "Ran npm
 * test", "Spawned Review discount fix" — rather than the function name the
 * model called (`edit(totals.ts)`, `shell(...)`, `spawn()`). The function
 * name stays in the row's accessible name and its expanded inspector.
 *
 * And how long it took: the durable start and end the server records for
 * every call.
 */

/** Live: the call is still preparing or running. Done: it finished, either way. */
export type ToolActionPhase = "live" | "done";

const VERBS: Record<string, readonly [live: string, done: string]> = {
  edit: ["Editing", "Edited"],
  find_replace: ["Editing", "Edited"],
  patch: ["Editing", "Edited"],
  move_code: ["Moving code in", "Moved code in"],
  write: ["Writing", "Wrote"],
  view: ["Reading", "Read"],
  read: ["Reading", "Read"],
  read_files: ["Reading", "Read"],
  grep: ["Searching", "Searched"],
  glob: ["Finding files", "Found files"],
  find_files: ["Finding files", "Found files"],
  ls: ["Listing", "Listed"],
  shell: ["Running", "Ran"],
  run_command: ["Running", "Ran"],
  shell_output: ["Reading output of", "Read output of"],
  shell_wait: ["Waiting for", "Waited for"],
  shell_kill: ["Stopping", "Stopped"],
  websearch: ["Searching the web for", "Searched the web for"],
  fetch: ["Fetching", "Fetched"],
  spawn: ["Spawning", "Spawned"],
  spawn_send: ["Messaging", "Messaged"],
  spawn_status: ["Checking", "Checked"],
  create_plan: ["Planning", "Planned"],
  update_plan: ["Updating the plan", "Updated the plan"],
  ask_user: ["Asking", "Asked"],
};

/** The tool's own name without an MCP server prefix (mcp__reliant__spawn → spawn). */
export function toolBaseName(rawName: string): string {
  return rawName.startsWith("mcp__") ? rawName.split("__").pop() || rawName : rawName;
}

/** The verb a row leads with, or null for a tool with no plain-words form. */
export function toolActionVerb(rawName: string, phase: ToolActionPhase): string | null {
  const verbs = VERBS[toolBaseName(rawName).toLowerCase()];
  if (!verbs) return null;
  return phase === "live" ? verbs[0] : verbs[1];
}

/**
 * Milliseconds between two recorded instants, or null when either is missing
 * or unreadable (or the clock ran backwards). Accepts ISO strings and epoch
 * milliseconds/seconds as strings, the shapes the stream carries.
 */
export function toolDurationMs(startedAt?: string, completedAt?: string): number | null {
  const start = parseInstant(startedAt);
  const end = parseInstant(completedAt);
  if (start === null || end === null || end < start) return null;
  return end - start;
}

function parseInstant(value?: string): number | null {
  if (!value) return null;
  if (/^\d+(\.\d+)?$/.test(value)) {
    const n = Number(value);
    // Seconds before 2001-09-09 in ms would be 10 digits; treat < 1e11 as seconds.
    return n < 1e11 ? n * 1000 : n;
  }
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
}

/** "320ms", "4.1s", "1m 05s", "1h 02m". */
export function formatToolDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`;
  const seconds = ms / 1000;
  if (seconds < 10) return `${seconds.toFixed(1)}s`;
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const totalSeconds = Math.round(seconds);
  const minutes = Math.floor(totalSeconds / 60);
  if (minutes < 60) return `${minutes}m ${String(totalSeconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}
