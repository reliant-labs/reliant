// Copyright (c) 2025 Reliant Labs

/**
 * The projects a new chat starts with beside its project (its primary): sent
 * as StartChatRequest.attached_project_ids on the first send, and shown as
 * chips beside the project chip (NewChatView, Chat/ChatProjectChips).
 *
 * Opened from the shell's choice of several projects (`?projects=`,
 * hooks/useChatProjectFilter), a new chat keeps them: its primary is the open
 * project when it is one of them, else the first (openChat
 * newChatTargetProjectId), and the rest are attached. Every project
 * (no filter) and one project attach nothing.
 *
 * The draft is read and written ONLY through getNewChatAttachedProjects /
 * useNewChatAttachedProjects and setNewChatAttachedProjects, so its source
 * can move without touching a caller. The URL is the truth: `/new?with=<id>,<id>`
 * names them in the order added (routeSchemas newChatSearchSchema). The `/new`
 * route applies it here (store/openChat.ts applyNewChat, ids the person
 * cannot see dropped), and mirrors every change back into it with a replace
 * (hooks/useChatRoute.ts), so a refresh or a shared link keeps a project
 * added by hand. This store is the tab's cache of it.
 */

import { create } from "zustand";
import type { SetUpJob } from "@/hooks/useChatProjects";

/** The most projects a chat may have attached (the server refuses an 11th). */
export const MAX_ATTACHED_PROJECTS = 10;

interface Draft {
  /** The draft's primary project; null before any new chat. */
  primaryId: string | null;
  /** Attached projects, in the order chosen; never the primary. */
  attached: readonly string[];
  setUpJob: SetUpJob | null;
}

const NONE: readonly string[] = [];

const useDraftStore = create<Draft>(() => ({ primaryId: null, attached: NONE, setUpJob: null }));

/** Setup belongs to the draft, not the composer instance rendering it. */
export function useNewChatSetUpJob(): SetUpJob | null {
  return useDraftStore((draft) => draft.setUpJob);
}

export function setNewChatSetUpJob(update: (job: SetUpJob | null) => SetUpJob | null): void {
  useDraftStore.setState((draft) => ({ setUpJob: update(draft.setUpJob) }));
}

/** `ids` without the primary and repeats, at most MAX_ATTACHED_PROJECTS. */
function attachable(primaryId: string | null, ids: readonly string[]): string[] {
  const out: string[] = [];
  for (const id of ids) {
    if (!id || id === primaryId || out.includes(id)) continue;
    out.push(id);
    if (out.length === MAX_ATTACHED_PROJECTS) break;
  }
  return out;
}

function attachedOf(draft: Draft, primaryId: string | null | undefined): readonly string[] {
  return primaryId && draft.primaryId === primaryId ? draft.attached : NONE;
}

/** The draft's attached projects for a new chat in `primaryId` ([] when the draft is another project's). */
export function getNewChatAttachedProjects(primaryId: string | null | undefined): readonly string[] {
  return attachedOf(useDraftStore.getState(), primaryId);
}

/** getNewChatAttachedProjects, re-rendering on a change. */
export function useNewChatAttachedProjects(primaryId: string | null | undefined): readonly string[] {
  return useDraftStore((draft) => attachedOf(draft, primaryId));
}

/** Make `ids` the draft's attached projects for a new chat in `primaryId` (null ends the draft). */
export function setNewChatAttachedProjects(primaryId: string | null, ids: readonly string[]): void {
  useDraftStore.setState({ primaryId, attached: primaryId ? attachable(primaryId, ids) : NONE, ...(!primaryId ? { setUpJob: null } : {}) });
}

/**
 * The projects attached by default to a new chat in `primaryId`, given the
 * shell's project filter (`filterIds`, [] for every project): the rest of a
 * choice of several that holds the primary; nothing otherwise.
 */
export function defaultAttachedProjects(primaryId: string | null, filterIds: readonly string[]): string[] {
  if (!primaryId || filterIds.length < 2 || !filterIds.includes(primaryId)) return [];
  return attachable(primaryId, filterIds);
}

/**
 * Get the draft ready for a new chat in `primaryId`. `keepDraft` keeps what
 * the person attached in the draft on screen (they changed its project with
 * the chip); otherwise the defaults apply (defaultAttachedProjects).
 */
export function prepareNewChatProjects(
  primaryId: string | null,
  filterIds: readonly string[],
  keepDraft: boolean,
): void {
  const draft = useDraftStore.getState();
  if (keepDraft && draft.primaryId !== null) {
    setNewChatAttachedProjects(primaryId, draft.attached);
    return;
  }
  setNewChatSetUpJob(() => null);
  setNewChatAttachedProjects(primaryId, defaultAttachedProjects(primaryId, filterIds));
}
