import { FileChangeStatus } from "../../../gen/reliant/v1/common_pb";
import type { ChangeGroup, FileChange } from "./types";

// Pure helpers that turn the server's file-change rows into what a row shows:
// which section it belongs to, a git-style status letter, and +/- line counts.
// Everything is derived from data the changes RPC already returns (status,
// is_new, and the diff text), so no extra backend call is needed.

export type StatusLetter = "M" | "A" | "D" | "R" | "U";

export const STATUS_LETTER_META: Record<StatusLetter, { label: string; className: string }> = {
  M: { label: "Modified", className: "text-warning" },
  A: { label: "Added", className: "text-success" },
  D: { label: "Deleted", className: "text-destructive" },
  R: { label: "Renamed", className: "text-info" },
  U: { label: "Untracked", className: "text-success" },
};

/** Section a file is listed under. DELETED (project changes only) lists with
 *  the other unstaged changes rather than disappearing. */
export function changeGroupOf(file: FileChange): ChangeGroup | null {
  switch (file.status) {
    case FileChangeStatus.STAGED:
      return "staged";
    case FileChangeStatus.MODIFIED:
    case FileChangeStatus.DELETED:
      return "modified";
    case FileChangeStatus.UNTRACKED:
      return "untracked";
    default:
      return null;
  }
}

/** Header lines of a unified diff: everything before the first hunk. */
function diffHeader(diff: string): string {
  const hunk = diff.indexOf("\n@@");
  return hunk >= 0 ? diff.slice(0, hunk) : diff.slice(0, 2000);
}

export function statusLetterOf(file: FileChange): StatusLetter {
  if (file.status === FileChangeStatus.UNTRACKED) return "U";
  if (file.status === FileChangeStatus.DELETED) return "D";
  const header = file.diff ? diffHeader(file.diff) : "";
  if (/^deleted file mode/m.test(header)) return "D";
  if (/^rename from /m.test(header)) return "R";
  if (/^new file mode/m.test(header)) return "A";
  if (file.status === FileChangeStatus.STAGED && file.is_new) return "A";
  return "M";
}

export interface LineCounts {
  added: number;
  removed: number;
}

const TRUNCATION_MARKER = "[diff truncated";

function countContentLines(content: string): number {
  if (content.length === 0) return 0;
  const lines = content.split("\n");
  return content.endsWith("\n") ? lines.length - 1 : lines.length;
}

/**
 * +/- line counts for a row, or null when they can't be known exactly
 * (no diff text, or the server truncated it to fit its transport budget).
 * Untracked files carry their raw content in `diff`, so every line is an
 * addition.
 */
export function lineCountsOf(file: FileChange): LineCounts | null {
  const diff = file.diff ?? "";
  if (diff.includes(TRUNCATION_MARKER)) return null;

  if (file.status === FileChangeStatus.UNTRACKED) {
    const raw = diff || file.content || "";
    return raw ? { added: countContentLines(raw), removed: 0 } : null;
  }

  if (!diff) {
    if (file.is_new && file.content) return { added: countContentLines(file.content), removed: 0 };
    return null;
  }

  let added = 0;
  let removed = 0;
  let inHunk = false;
  for (const line of diff.split("\n")) {
    if (line.startsWith("@@")) {
      inHunk = true;
      continue;
    }
    if (!inHunk) continue;
    if (line.startsWith("+")) added++;
    else if (line.startsWith("-")) removed++;
  }
  return inHunk ? { added, removed } : null;
}

export function splitPath(path: string): { name: string; dir: string } {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? { name: path, dir: "" } : { name: path.slice(slash + 1), dir: path.slice(0, slash) };
}

export function pluralize(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`;
}
