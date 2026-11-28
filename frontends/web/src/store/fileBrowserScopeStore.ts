// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

/**
 * What the Files tab shows:
 * - `project`: the project or worktree tree (the default; editable).
 * - `scratch`: this chat's scratch directory, ~/.reliant/scratch/<chat>, where
 *   agents keep briefings and outputs outside the repository.
 * - `machine`: the whole machine that holds the workspace, from the home
 *   directory up to "/" (read-only: files open in the viewer and download).
 */
export type FileBrowserScope = "project" | "scratch" | "machine";

interface FileBrowserScopeState {
  scope: FileBrowserScope;
  /**
   * The directory each machine-wide scope shows, as ListMachineDirectory
   * takes it (absolute, "~/…", or relative to the workspace root). null is
   * the scope's own root: the home directory, or the chat's scratch directory.
   */
  paths: { machine: string | null; scratch: string | null };
  /** The entry a reveal points at, highlighted in the machine view: an absolute path or a name. */
  highlight: string | null;

  setScope: (scope: FileBrowserScope) => void;
  /** Shows `path` in a machine-wide scope (the current one when omitted). */
  navigate: (scope: "machine" | "scratch", path: string | null) => void;
  /**
   * "Pop out" of the project: switches to the Machine scope at `path` (".." is
   * the folder the workspace is in), highlighting `highlight` there.
   */
  browseMachine: (path: string, highlight?: string | null) => void;
  /** Back to the project tree with every machine-wide scope at its root. */
  reset: () => void;
}

const initial = { scope: "project" as FileBrowserScope, paths: { machine: null, scratch: null }, highlight: null };

export const useFileBrowserScopeStore = create<FileBrowserScopeState>()((set) => ({
  ...initial,
  setScope: (scope) => set({ scope, highlight: null }),
  navigate: (scope, path) => set((s) => ({ paths: { ...s.paths, [scope]: path }, highlight: null })),
  browseMachine: (path, highlight = null) =>
    set((s) => ({ scope: "machine", paths: { ...s.paths, machine: path }, highlight })),
  reset: () => set({ ...initial, paths: { machine: null, scratch: null } }),
}));

/** The chat's scratch directory, as the prompt names it to its agents. */
export function chatScratchPath(chatId: string): string {
  return `~/.reliant/scratch/${chatId}`;
}

/** The parent of an absolute path ("/" stays "/"). */
export function parentPath(path: string): string {
  const trimmed = path.length > 1 ? path.replace(/\/+$/, "") : path;
  const i = trimmed.lastIndexOf("/");
  if (i <= 0) return "/";
  return trimmed.slice(0, i);
}

/** Breadcrumb segments of an absolute path: "/" first, then each folder with its full path. */
export function pathSegments(path: string): Array<{ name: string; path: string }> {
  const out = [{ name: "/", path: "/" }];
  let acc = "";
  for (const part of path.split("/").filter(Boolean)) {
    acc += `/${part}`;
    out.push({ name: part, path: acc });
  }
  return out;
}
