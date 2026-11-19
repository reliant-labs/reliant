/**
 * What a quiet tool row reads off its call: the icon for its kind of work, and
 * an edit's line counts (`+4 −1`).
 */

import { diffLines } from "diff";
import {
  BookOpen,
  Bot,
  FilePen,
  FileText,
  FolderSearch,
  Globe,
  Image,
  ListTodo,
  MessageCircleQuestion,
  Search,
  Server,
  Terminal,
  Workflow,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { toolBaseName } from "../../lib/toolAction";

const ICONS: Record<string, LucideIcon> = {
  view: FileText,
  read: FileText,
  read_files: FileText,
  edit: FilePen,
  write: FilePen,
  patch: FilePen,
  find_replace: FilePen,
  move_code: FilePen,
  grep: Search,
  glob: FolderSearch,
  find_files: FolderSearch,
  ls: FolderSearch,
  code_context: Search,
  shell: Terminal,
  run_command: Terminal,
  shell_output: Terminal,
  shell_wait: Terminal,
  shell_kill: Terminal,
  websearch: Globe,
  fetch: Globe,
  spawn: Bot,
  spawn_send: Bot,
  spawn_status: Bot,
  spawn_stop: Bot,
  create_plan: ListTodo,
  update_plan: ListTodo,
  add_task: ListTodo,
  update_task: ListTodo,
  list_tasks: ListTodo,
  skill: BookOpen,
  load_tool: BookOpen,
  ask_user: MessageCircleQuestion,
  generate_image: Image,
  generate_video: Image,
  request_machine: Server,
  draft_automation: Workflow,
};

/** The icon a tool's row leads with; a wrench for any tool it does not know. */
export function toolRowIcon(rawName: string): LucideIcon {
  return ICONS[toolBaseName(rawName).toLowerCase()] ?? Wrench;
}

export interface DiffStat {
  added: number;
  removed: number;
}

const EDIT_TOOLS = new Set(["edit", "write", "patch", "find_replace"]);

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function addPair(stat: DiffStat, before: string, after: string): void {
  if (!before && !after) return;
  for (const change of diffLines(before, after)) {
    const lines = change.count ?? change.value.split("\n").filter(Boolean).length;
    if (change.added) stat.added += lines;
    else if (change.removed) stat.removed += lines;
  }
}

/**
 * The lines an edit adds and removes, from its input: `old_string` /
 * `new_string`, an `edits` array of them, or a write's `content` (all
 * added). Null for a tool that is not an edit, or an input with nothing to
 * count (a skeleton the transcript left out, a call still streaming).
 */
export function editDiffStat(rawName: string, input: unknown, inputElided = false): DiffStat | null {
  if (inputElided || !EDIT_TOOLS.has(toolBaseName(rawName).toLowerCase())) return null;
  const data = asRecord(input);
  if (!data) return null;
  const stat: DiffStat = { added: 0, removed: 0 };
  let edits: unknown = data.edits;
  if (typeof edits === "string") {
    try {
      edits = JSON.parse(edits);
    } catch {
      edits = undefined;
    }
  }
  if (Array.isArray(edits) && edits.length > 0) {
    for (const edit of edits) {
      const e = asRecord(edit);
      if (e) addPair(stat, str(e.old_string), str(e.new_string));
    }
  } else {
    addPair(stat, str(data.old_string), str(data.new_string) || str(data.content) || str(data.Content));
  }
  return stat.added === 0 && stat.removed === 0 ? null : stat;
}
