// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox's data layer (WORKFLOW_UI.md §8): one list of everything waiting
 * on the user, and the counts the nav badge shows. Both are scoped the same
 * way — the current project, or every project — so the badge never counts
 * what the list is not showing.
 *
 * Both are one query per scope: the badge selects the counts from the list.
 * It does not poll. Approvals, questions and waiting-for-machine changes
 * arrive as `chat_activity_changed`, and a run stopping arrives as
 * `chat_state_change`; the global updates store asks `inboxInvalidatingUpdate`
 * and marks the query stale (refetching at most once per
 * STREAM_REFETCH_INTERVAL_MS, longer when ListInbox itself is slow, and not
 * at all while the tab is hidden). The automation failure kinds emit NO user
 * update of their own, so a returning user must not see an old list: the
 * read goes stale after INBOX_STALE_MS, and the window-focus refetch then
 * reads it again (see INBOX_STALE_MS for why not on every focus).
 */

import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { create as createStore } from "zustand";
import { persist } from "zustand/middleware";

import { InboxCategory, InboxItemKind, type InboxItem, type ListInboxResponse } from "../gen/reliant/v1/inbox_pb";
import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import { orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "../lib/orgScope";
import { queryClient as appQueryClient } from "../lib/query-client";
import { useProjectStore } from "../store/projectStore";
import {
  inboxServiceKeys,
  inboxServiceQueries,
  useCleanupStorage as useCleanupStorageRpc,
  useForgetStorageWorktree as useForgetStorageWorktreeRpc,
  useKeepMarkedWorktree as useKeepMarkedWorktreeRpc,
  useReclaimMarkedWorktreeNow as useReclaimMarkedWorktreeNowRpc,
  useDismissInboxItem,
  useRestoreInboxItem,
} from "./inbox-service-hooks_gen";
import { useDecideWorktreeReclaim as useDecideWorktreeReclaimRpc } from "./worktree-reclaim-service-hooks_gen";

export type { InboxItem, ListInboxResponse } from "../gen/reliant/v1/inbox_pb";
export { InboxCategory, InboxItemKind } from "../gen/reliant/v1/inbox_pb";

// ── Scope ───────────────────────────────────────────────────────────────────

/** "project": only the current project's items. "all": every project's. */
export type InboxScope = "project" | "all";

interface InboxScopeState {
  scope: InboxScope;
  setScope: (scope: InboxScope) => void;
}

/** The user's last choice, remembered across sessions. Defaults to the project. */
export const useInboxScopeStore = createStore<InboxScopeState>()(
  persist(
    (set) => ({
      scope: "project",
      setScope: (scope) => set({ scope }),
    }),
    { name: "reliant-inbox-scope" },
  ),
);

/**
 * The project the Inbox is scoped to, or undefined for every project. With no
 * current project there is nothing to scope to, so "project" falls back to all.
 *
 * `currentProjectId` is the page's own current project when it resolves one
 * (the Inbox page: the shell's, useShellCurrentProject; null for none);
 * omitted, it is the context project.
 */
export function useInboxProjectId(currentProjectId?: string | null): string | undefined {
  const scope = useInboxScopeStore((state) => state.scope);
  const contextProjectId = useProjectStore((state) => state.currentProject?.id);
  const projectId = currentProjectId === undefined ? contextProjectId : (currentProjectId ?? undefined);
  return scope === "project" ? projectId : undefined;
}

// ── Keys ────────────────────────────────────────────────────────────────────
//
// The Inbox is InboxService on forge's generated keys: one ListInbox entry per
// scope (`inboxServiceKeys.listInbox({ projectId })`), every one of them under
// `inboxServiceKeys.listInbox.all`. Every write below is a generated mutation,
// which invalidates `inboxServiceKeys.all` on success; the stream invalidates
// the same root (store/streamInvalidatedScopes, `inbox`).

/**
 * Whether a user update can change what the Inbox holds. Exported so the
 * global updates store asks this module rather than keeping its own list.
 * An async question (ask_user without blocking: true) moves no chat activity, so its
 * coming and going is announced as a refetch of type "inbox".
 */
export function inboxInvalidatingUpdate(type: UserUpdateType, data?: unknown): boolean {
  if (type === UserUpdateType.REFETCH) {
    const payload = typeof data === "string" ? safeParse(data) : data;
    return (payload as { type?: unknown } | null)?.type === "inbox";
  }
  return type === UserUpdateType.CHAT_ACTIVITY_CHANGED || type === UserUpdateType.CHAT_STATE_CHANGE;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

export interface InboxCounts {
  /** Approvals + questions + waiting-for-machine: the items blocking a live run. */
  blockingCount: number;
  /** Any automation failure is waiting (the badge's dot, not a count). */
  hasInformational: boolean;
  /**
   * A machine has housekeeping waiting (the Agent sandboxes nav row's dot):
   * the items the person's filters kept out of the list, or listed ones when
   * their "housekeeping" filter is on.
   */
  hasHousekeeping: boolean;
}

// ── Query hooks ─────────────────────────────────────────────────────────────

/**
 * The one Inbox read: items (default limit, 100) and the counts, in the tab's
 * org (lib/orgScope), which the request and so the key name.
 */
function inboxRequest(projectId: string | undefined, orgId: string | null) {
  return orgRequest({ projectId }, orgId);
}

/**
 * How old the Inbox may be when the window regains focus before it is read
 * again. It used to refetch on EVERY focus (`refetchOnWindowFocus: "always"`),
 * for the automation failure kinds, which no update announces. That made each
 * tab switch a ListInbox — a read that took 1.5–3 s on the server under load
 * (2026-11-17) — even when the list had been read a second earlier. Short
 * against the 30 s default, so the failure kinds still show within seconds
 * of the user coming back.
 */
export const INBOX_STALE_MS = 10_000;

/** The Inbox's items in the server's priority order (default limit, 100). */
export function useInbox(projectId?: string) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      inboxServiceQueries.listInbox(inboxRequest(projectId, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: INBOX_STALE_MS,
      }),
      org,
    ),
  );
}

function countsOf(response: ListInboxResponse): InboxCounts {
  return {
    blockingCount: response.blockingCount,
    hasInformational: response.hasInformational,
    hasHousekeeping:
      response.housekeepingCount > 0 || response.items.some((item) => item.category === InboxCategory.HOUSEKEEPING),
  };
}

/**
 * The nav badge's counts, read from the SAME query as the list. The server
 * counts every item before it truncates the list, so the list response's
 * counts are the badge's; a separate `limit: 0` read doubled every Inbox
 * refetch for a few KB of items saved.
 */
export function useInboxCounts(projectId?: string) {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      inboxServiceQueries.listInbox(inboxRequest(projectId, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: INBOX_STALE_MS,
        select: countsOf,
      }),
      org,
    ),
  );
}

// ── Machine housekeeping ────────────────────────────────────────────────────
//
// The Inbox keeps a machine's housekeeping out unless the person asks for it
// (InboxCategory.HOUSEKEEPING); each sandbox's settings page lists it instead,
// with every action the Inbox row has. One read serves every machine: the
// sandboxes list (a dot per machine) and each machine's page select from the
// same ListInbox entry, which every Inbox write and stream update
// invalidates with the rest (it is under `inboxServiceKeys.listInbox.all`).

/** What the machine pages ask for, regardless of Inbox preferences. */
export const MACHINE_HOUSEKEEPING_CATEGORIES: InboxCategory[] = [InboxCategory.HOUSEKEEPING];

/**
 * The machine an item is housekeeping of: its daemon id ("" for the
 * workspaces no machine is on record for), or null when the item is not
 * about a machine's storage or worktrees. By kind, not category, so a server
 * that predates `categories` (and answers with the whole Inbox) still files
 * the same items.
 */
export function housekeepingMachineOf(item: InboxItem): string | null {
  switch (item.payload.case) {
    case "storage":
    case "worktreeReclaim":
    case "worktreeMarked":
      return item.payload.value.daemonId;
    default:
      return null;
  }
}

const HOUSEKEEPING_KINDS = new Set([InboxItemKind.STORAGE, InboxItemKind.WORKTREE_RECLAIM, InboxItemKind.WORKTREE_MARKED]);

/** Every machine's housekeeping items, by daemon id ("" holds the ones no machine is on record for). */
export type MachineHousekeeping = ReadonlyMap<string, InboxItem[]>;

function housekeepingByMachine(response: ListInboxResponse): MachineHousekeeping {
  const byMachine = new Map<string, InboxItem[]>();
  for (const item of response.items) {
    if (!HOUSEKEEPING_KINDS.has(item.kind)) continue;
    const machine = housekeepingMachineOf(item);
    if (machine === null) continue;
    const list = byMachine.get(machine);
    if (list) list.push(item);
    else byMachine.set(machine, [item]);
  }
  return byMachine;
}

const NO_HOUSEKEEPING: MachineHousekeeping = new Map();
const NO_ITEMS: InboxItem[] = [];

/**
 * Every machine's housekeeping, read once (ListInbox with
 * MACHINE_HOUSEKEEPING_CATEGORIES, in the tab's org: a machine's items list
 * in every org). Undefined until it has loaded.
 */
export function useMachineHousekeeping() {
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      inboxServiceQueries.listInbox(orgRequest({ categories: MACHINE_HOUSEKEEPING_CATEGORIES }, org.orgId), {
        enabled: orgQueryEnabled(org),
        staleTime: INBOX_STALE_MS,
        select: housekeepingByMachine,
      }),
      org,
    ),
  );
}

/** One machine's housekeeping items ("" for the workspaces no machine is on record for); none until loaded. */
export function useMachineHousekeepingItems(daemonId: string): InboxItem[] {
  const query = useMachineHousekeeping();
  return (query.data ?? NO_HOUSEKEEPING).get(daemonId) ?? NO_ITEMS;
}

// ── Cache helpers ───────────────────────────────────────────────────────────

/** Drop items from every cached list (each scope holds its own copy). */
function dropFromLists(client: QueryClient, ids: Set<string>): void {
  client.setQueriesData<ListInboxResponse>({ queryKey: inboxServiceKeys.listInbox.all }, (previous) =>
    previous ? { ...previous, items: previous.items.filter((item) => !ids.has(item.itemId)) } : previous,
  );
}

/** Mark every Inbox read stale; observed ones refetch. */
export function invalidateInbox(client: QueryClient = appQueryClient): Promise<void> {
  return client.invalidateQueries({ queryKey: inboxServiceKeys.all });
}

/**
 * Take items out of the cached lists at once — after the user acted on them,
 * or learned they were already handled — and mark every inbox query stale so
 * the counts and anything that replaced them come from the server.
 */
export function removeInboxItems(itemIds: string[], client = appQueryClient): void {
  dropFromLists(client, new Set(itemIds));
  void invalidateInbox(client);
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/**
 * Dismiss items — one row, or a whole section: `mutate({ itemIds })`. They
 * leave every cached list immediately; on error the lists are put back and
 * read again. Any kind can be dismissed: hiding an approval or a question does
 * not resolve it.
 */
export function useDismissInboxItems() {
  const queryClient = useQueryClient();
  return useDismissInboxItem({
    onMutate: async ({ itemIds = [] }) => {
      await queryClient.cancelQueries({ queryKey: inboxServiceKeys.listInbox.all });
      const previous = queryClient.getQueriesData<ListInboxResponse>({ queryKey: inboxServiceKeys.listInbox.all });
      dropFromLists(queryClient, new Set(itemIds));
      return { previous };
    },
    onError: (_error, _request, context) => {
      for (const [key, data] of context?.previous ?? []) queryClient.setQueryData(key, data);
      void invalidateInbox(queryClient);
    },
  });
}

/** Undo a dismissal (`mutate({ itemIds })`): the items come back on the next read. */
export const useRestoreInboxItems = useRestoreInboxItem;

/**
 * Clean up a machine's held worktrees: the machine saves each one's work to a
 * local ref, verifies it, and removes the directory. The caller passes exactly
 * the ids the user confirmed. Whatever the machine reports, the storage item is
 * read again afterwards (the generated write on success, here on failure), so
 * the list shows what is actually left.
 */
export function useCleanupStorage() {
  const queryClient = useQueryClient();
  return useCleanupStorageRpc({
    onError: () => {
      void invalidateInbox(queryClient);
    },
  });
}

/**
 * Answer a machine's worktree-reclaim item (`mutate({ daemonId, decisions })`):
 * remove or keep each proposed worktree, as shown. The machine records each
 * decision only while the worktree is still in the state shown. The inbox is
 * read again either way: the item lists what the machine proposes now.
 */
export function useDecideWorktreeReclaim() {
  const queryClient = useQueryClient();
  return useDecideWorktreeReclaimRpc({
    onSettled: () => {
      void invalidateInbox(queryClient);
    },
  });
}

/**
 * Forget an archived workspace no machine is on record for and no connected
 * machine has (`mutate({ worktreeId })`): Reliant drops its record of the
 * folder and deletes nothing. Refused unless the server still finds it
 * forgettable; the inbox is read again either way.
 */
export function useForgetStorageWorktree() {
  const queryClient = useQueryClient();
  return useForgetStorageWorktreeRpc({
    onError: () => {
      void invalidateInbox(queryClient);
    },
  });
}

/**
 * Keep a worktree marked to be reclaimed (`mutate({ worktreeId, markId })`):
 * the mark shown is cancelled and its tombstone removed. The inbox is read
 * again either way.
 */
export function useKeepMarkedWorktree() {
  const queryClient = useQueryClient();
  return useKeepMarkedWorktreeRpc({
    onSettled: () => {
      void invalidateInbox(queryClient);
    },
  });
}

/**
 * Reclaim a marked worktree now (`mutate({ worktreeId, markId })`): the
 * mark's wait is skipped and the worktree checked at once; one used since it
 * was marked is kept. The inbox is read again either way.
 */
export function useReclaimMarkedWorktreeNow() {
  const queryClient = useQueryClient();
  return useReclaimMarkedWorktreeNowRpc({
    onSettled: () => {
      void invalidateInbox(queryClient);
    },
  });
}
