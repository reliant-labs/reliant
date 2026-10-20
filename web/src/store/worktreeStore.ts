import { create } from 'zustand';
import {
  worktreeGrpc,
  type Worktree as GrpcWorktree,
  type DiscoveredWorktree as GrpcDiscoveredWorktree,
  type StaleWorktree as GrpcStaleWorktree,
  type CleanupMetadata,
} from '../api/worktree-grpc';
import { WorktreeStatus } from '../gen/reliant/v1/worktree_pb';
import { toast } from '../lib/toast-manager';
import { useChatStore } from './chatStore';
import { getChatFromCache } from '../hooks/chat-queries';
import { useChatNavigationStore } from './chatNavigationStore';
import { useWorkspaceStateStore } from './workspaceStateStore';
import { useBrowserStore } from './browserStore';
import { Code, ConnectError } from "@connectrpc/connect";
import { logger } from '../lib/logger';
import { singleflight } from '../lib/singleflight';
import { subscribeToRefetch, type RefetchEvent } from './refetchStore';
import { retryAcrossWake } from '../lib/daemon-retry';

// Re-export CleanupMetadata from gRPC types
export type { CleanupMetadata };

/**
 * Said once when a workspace action found its machine asleep: the server woke
 * it, and the action finishes by itself once the machine is back, so this is
 * news rather than an error.
 */
function announceMachineWaking() {
  toast.info('Waking up your machine — this will finish on its own.');
}

export interface Worktree {
  id: string;
  name: string;
  path: string;
  branch: string;
  base_branch: string;
  project_id?: string;
  chat_id?: string;
  session_id?: string;
  status: WorktreeStatus;
  is_main: boolean; // TRUE for the main/default project worktree
  copy_files?: string[]; // Exact paths from the workspace root (e.g. ".env", "web/node_modules") — never searched for
  created_at: string;
  updated_at: string;
  last_active: string;
  deleted_at?: string | null; // Archive timestamp
  cleanup_metadata?: CleanupMetadata | null; // Tracks what was deleted during archive
  failure_reason?: string; // Why a FAILED create failed, when it is the user's to fix
}

// Create-only inputs. base_branches carries per-repo base-branch overrides
// (key = repo_id) for multi-repo projects; the server resolves them into each
// repo's checkout and does not echo them back on the Worktree.
export type CreateWorktreeInput = Partial<Worktree> & {
  force?: boolean;
  source_worktree_id?: string;
  base_branches?: Record<string, string>;
};

export type DiscoveredWorktree = GrpcDiscoveredWorktree;
export type StaleWorktree = GrpcStaleWorktree;

// Convert gRPC worktree to store worktree format
function grpcToStore(grpc: GrpcWorktree): Worktree {
  return {
    id: grpc.id,
    name: grpc.name,
    path: grpc.path,
    branch: grpc.branch,
    base_branch: grpc.base_branch,
    project_id: grpc.project_id,
    chat_id: grpc.chat_id,
    status: grpc.status,
    is_main: grpc.is_main,
    created_at: grpc.created_at,
    updated_at: grpc.updated_at,
    last_active: grpc.last_active,
    deleted_at: grpc.deleted_at || null,
    cleanup_metadata: grpc.cleanup_metadata || null,
    failure_reason: grpc.failure_reason,
  };
}

// Extract error message from various error types
function getErrorMessage(error: unknown, defaultMsg: string): string {
  if (error instanceof ConnectError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return defaultMsg;
}

interface WorktreeStore {
  // The worktrees the app knows about: the project's main workspace, whatever
  // list pages have been loaded, and any fetched by id. NOT every workspace of
  // the project — a project can hold hundreds that agents created, and the
  // sidebar needs none of them (the chat list carries each chat's workspace).
  worktrees: Worktree[];
  currentWorktree: Worktree | null;
  discoveredWorktrees: DiscoveredWorktree[];
  isLoading: boolean;
  // A list page has loaded (loadWorktrees / refreshWorktrees).
  hasLoaded: boolean;
  // Set while more live workspaces follow the pages loaded so far.
  nextPageToken: string;
  isLoadingMore: boolean;
  // The archived workspaces, a separate query loaded only by the archived view.
  archivedWorktrees: Worktree[];
  archivedNextPageToken: string;
  hasLoadedArchived: boolean;
  isLoadingArchived: boolean;
  isDiscovering: boolean;
  staleWorktrees: StaleWorktree[];
  workspacesRoot: string;
  deletingId: string | null;
  error: string | null;

  // Fetches just the project's main workspace and selects it when nothing is
  // selected. What app start and project switches use: one cheap row, however
  // many workspaces the project holds.
  loadMainWorktree: (projectId?: string) => Promise<void>;
  // Loads the first page of the project's live workspaces (main first). For
  // consumers that list workspaces without chats: pickers, the Workspaces page.
  loadWorktrees: (projectId?: string) => Promise<void>;
  // Like loadWorktrees, but never joins an in-flight request. Use after a
  // mutation whose effects must be observed: a singleflight-deduplicated load
  // can return data fetched BEFORE the mutation landed.
  refreshWorktrees: (projectId?: string) => Promise<void>;
  // Appends the next page of live workspaces; a no-op on the last page.
  loadMoreWorktrees: (projectId?: string) => Promise<void>;
  // Loads the first page of archived workspaces.
  loadArchivedWorktrees: (projectId?: string) => Promise<void>;
  loadMoreArchivedWorktrees: (projectId?: string) => Promise<void>;
  // The worktree with this id, fetched when the store does not hold it; null
  // only when the server does not know it either. For opening a workspace
  // (selecting it, reading its path) — never for grouping chats.
  resolveWorktree: (worktreeId: string) => Promise<Worktree | null>;
  selectWorktree: (worktree: Worktree | null, options?: { skipWorkspaceStateSave?: boolean }) => void;
  createWorktree: (data: CreateWorktreeInput) => Promise<Worktree>;
  importWorktree: (data: {
    path: string;
    name?: string;
    project_id: string;
    repo_id: string;
    confirm_move: boolean;
  }) => Promise<Worktree>;
  discoverWorktrees: (projectId: string) => Promise<void>;
  pruneWorktrees: (projectId: string) => Promise<StaleWorktree[]>;
  // ONLY archives (sets deleted_at). Never permanently deletes.
  // Optional cleanup: delete local directory and/or git branch
  archiveWorktree: (id: string, options?: { deleteGitBranch?: boolean }) => Promise<void>;
  // Archives worktree (sets deleted_at) if not already archived.
  // Permanently deletes from database if already archived.
  // Optional cleanup: delete local directory and/or git branch
  deleteWorktree: (id: string, options?: { deleteGitBranch?: boolean }) => Promise<void>;
  unarchiveWorktree: (id: string) => Promise<void>;
  updateWorktreeStatus: (id: string, status: Worktree['status']) => Promise<void>;
  // Selects the project's last-used workspace, or main. Fetches a workspace the
  // store does not hold by id; resolves false when nothing could be selected.
  restoreLastWorktree: (projectId: string) => Promise<boolean>;
  switchWorktreeContext: (projectId: string, worktree: Worktree | null, options?: { openFreshNewChat?: boolean }) => Promise<void>;
  reset: () => void;
}

/**
 * Carry the selected worktree over to the freshly loaded row with the same id.
 *
 * `currentWorktree` is a snapshot object, so replacing the list alone leaves it
 * frozen at whatever the server said when it was selected. For a worktree
 * selected straight out of CreateWorktree that is CREATING with an empty path,
 * forever — every reader of `currentWorktree.path` then saw "" long after the
 * directory existed. Identity is kept when nothing changed, so a routine
 * reload does not re-run effects keyed on the object.
 *
 * A selection absent from the loaded list (e.g. an archived worktree after an
 * active-only load) is left alone; archive/delete own that transition.
 */
function refreshSelection(current: Worktree | null, loaded: Worktree[]): Worktree | null {
  if (!current) return current;
  const fresh = loaded.find((w) => w.id === current.id);
  if (!fresh) return current;
  const changed =
    fresh.path !== current.path ||
    fresh.status !== current.status ||
    fresh.branch !== current.branch ||
    fresh.base_branch !== current.base_branch ||
    fresh.name !== current.name ||
    fresh.deleted_at !== current.deleted_at ||
    fresh.failure_reason !== current.failure_reason;
  return changed ? fresh : current;
}

/**
 * Reload worktrees when the server settles one that is still CREATING.
 *
 * CreateWorktree returns before the workspace exists on disk — the row comes
 * back CREATING with an empty path, and the server flips it to ACTIVE (with
 * the path) or FAILED later, announcing that with a `worktree_changes` refetch
 * scoped to the worktree id. Without this, the store never learned the path.
 *
 * worktree_changes also fires on every agent tool call, so the reload is gated
 * on the event naming a worktree we know to be pending; anything else is file
 * churn that the changes/file-tree subscribers already handle.
 */
function handleWorktreeRefetch(event: RefetchEvent): void {
  if (!event.entityId) return;
  const { worktrees } = useWorktreeStore.getState();
  const pending = worktrees.find(
    (w) => w.id === event.entityId && w.status === WorktreeStatus.CREATING
  );
  if (!pending) return;
  logger.info('[WorktreeStore] Pending worktree changed, refreshing', {
    worktreeId: pending.id,
    name: pending.name,
  });
  void refreshWorktreeById(pending.id).then((settled) => {
    if (settled?.status === WorktreeStatus.FAILED) announceCreateFailed(settled);
    else if (settled?.status === WorktreeStatus.ACTIVE) announceCreated(settled);
  });
}

// One toast per create: pending, then success or failure, replacing each other
// in place under this id.
const createToastId = (worktreeId: string) => `worktree-create:${worktreeId}`;

// Ids already announced: two refetches can land while a row is still CREATING.
const announcedCreateFailures = new Set<string>();
const announcedCreates = new Set<string>();

function announceCreatePending(worktree: Worktree): void {
  void toast.loading(`Creating workspace "${worktree.name}"…`, {
    id: createToastId(worktree.id),
  });
}

function announceCreated(worktree: Worktree): void {
  if (announcedCreates.has(worktree.id)) return;
  announcedCreates.add(worktree.id);
  toast.success(`Workspace "${worktree.name}" created successfully`, {
    id: createToastId(worktree.id),
    duration: 4000,
  });
}

/**
 * Say why a create failed. It settles in the background, so without this the
 * only sign was a "Failed" badge somewhere in the workspace list. The server
 * writes the reason on the row when it is the user's to fix ("base branch 'x'
 * doesn't exist in reliant — check the name ...") and leaves it empty when the
 * failure was ours, which is reported on our side.
 */
function announceCreateFailed(worktree: Worktree): void {
  if (announcedCreateFailures.has(worktree.id)) return;
  announcedCreateFailures.add(worktree.id);
  void toast.error(
    worktree.failure_reason
      ? `Couldn't create workspace "${worktree.name}": ${worktree.failure_reason}`
      : `Couldn't create workspace "${worktree.name}": creating it did not finish. Archive it and try again.`,
    { id: createToastId(worktree.id), duration: 10000 },
  );
}

// Monotonic ticket per fetch, so a slow response cannot overwrite a newer one.
// Responses can land out of order — a refresh issued after a mutation often
// resolves before the pre-mutation load it raced — and applying them by
// arrival order silently restores stale data.
let loadTicket = 0;
let lastAppliedTicket = 0;
// Bumped whenever the list is replaced or reset, so a "load more" that was in
// flight for the old list is dropped instead of appended to the new one.
let listGeneration = 0;
let archivedGeneration = 0;

// Ids resolveWorktree must not ask for again: the server said they are gone,
// or a request for them is in flight.
const unresolvableWorktreeIds = new Set<string>();
const resolvingWorktrees = new Map<string, Promise<Worktree | null>>();

function upsertWorktrees(existing: Worktree[], incoming: Worktree[]): Worktree[] {
  if (incoming.length === 0) return existing;
  const fresh = new Map(incoming.map((w) => [w.id, w]));
  const merged = existing.map((w) => fresh.get(w.id) ?? w);
  const have = new Set(existing.map((w) => w.id));
  return [...merged, ...incoming.filter((w) => !have.has(w.id))];
}

/** Select the project's main workspace when nothing is selected yet. */
function autoSelectMain(projectId: string, candidates: Worktree[]): void {
  if (useWorktreeStore.getState().currentWorktree) return;
  const mainWorktree = candidates.find((w) => w.is_main && w.project_id === projectId);
  if (!mainWorktree) return;
  logger.info('[WorktreeStore] Auto-selecting main worktree after load', {
    worktreeId: mainWorktree.id,
    name: mainWorktree.name,
  });
  useWorktreeStore.getState().selectWorktree(mainWorktree, { skipWorkspaceStateSave: true });
}

/**
 * Fetch the project's main workspace alone and merge it into the store. One
 * row however many workspaces the project holds: this is all app start needs,
 * because the sidebar groups from the chat list and the full list is only for
 * consumers that show workspaces without chats.
 */
async function fetchMainInto(projectId: string, dedupe: boolean): Promise<void> {
  const ticket = ++loadTicket;
  const isStale = () => ticket < lastAppliedTicket;
  try {
    const fetchMain = async () => {
      const main = await worktreeGrpc.getMain(projectId);
      return main ? grpcToStore(main) : null;
    };
    const main = dedupe ? await singleflight(`loadMainWorktree:${projectId}`, fetchMain) : await fetchMain();
    if (isStale()) return;
    lastAppliedTicket = ticket;
    useWorktreeStore.setState((state) => ({
      worktrees: main ? upsertWorktrees(state.worktrees, [main]) : state.worktrees,
      currentWorktree: main ? refreshSelection(state.currentWorktree, [main]) : state.currentWorktree,
      hasLoaded: true,
      error: null,
    }));
    if (main) autoSelectMain(projectId, [main]);
  } catch (error) {
    if (isStale()) return;
    lastAppliedTicket = ticket;
    useWorktreeStore.setState({
      error: getErrorMessage(error, 'Failed to load workspace'),
      hasLoaded: true,
    });
  }
}

/**
 * Fetch the first page of the project's live workspaces and make it the list.
 *
 * `dedupe` controls whether the request may join an identical in-flight one.
 * Reads set it (cheap, and the common case is several components mounting at
 * once); post-mutation refreshes must not, because an in-flight request that
 * started before the mutation would resolve with pre-mutation data.
 */
async function fetchFirstPageInto(projectId: string, dedupe: boolean): Promise<void> {
  const set = useWorktreeStore.setState;
  const ticket = ++loadTicket;
  set({ isLoading: true, error: null });

  // A response is stale if a later request has already been applied. Stale
  // responses are dropped entirely, including their isLoading reset — the
  // newer request still in flight owns that.
  const isStale = () => ticket < lastAppliedTicket;

  try {
    const fetchPage = async () => {
      const page = await worktreeGrpc.list(projectId);
      return { worktrees: page.worktrees.map(grpcToStore), nextPageToken: page.nextPageToken };
    };

    const page = dedupe ? await singleflight(`loadWorktrees:${projectId}`, fetchPage) : await fetchPage();

    if (isStale()) {
      logger.info('[WorktreeStore] Dropping stale worktree load', { projectId, ticket, lastAppliedTicket });
      return;
    }
    lastAppliedTicket = ticket;
    listGeneration++;

    set((state) => {
      // The page replaces the list, except the selected workspace: it may sit
      // beyond the first page, and dropping it would unselect what the user
      // is working in.
      const selected = state.currentWorktree;
      const keepSelected =
        selected && selected.project_id === projectId && !page.worktrees.some((w) => w.id === selected.id);
      const worktrees = keepSelected ? [...page.worktrees, selected] : page.worktrees;
      return {
        worktrees,
        currentWorktree: refreshSelection(state.currentWorktree, worktrees),
        nextPageToken: page.nextPageToken,
        isLoading: false,
        hasLoaded: true,
      };
    });

    autoSelectMain(projectId, page.worktrees);
  } catch (error) {
    if (isStale()) return;
    lastAppliedTicket = ticket;
    set({
      error: getErrorMessage(error, 'Failed to load worktrees'),
      isLoading: false,
      hasLoaded: true,
    });
  }
}

/**
 * True when the server has said this workspace does not exist (or is not the
 * caller's). A failed fetch is NOT that: resolveWorktree returns null for a
 * network error too, and acting on absence would discard live state.
 */
export function isWorktreeKnownGone(worktreeId: string): boolean {
  return unresolvableWorktreeIds.has(worktreeId);
}

/** A workspace the store holds, live or archived. */
function findKnownWorktree(id: string): Worktree | undefined {
  const { worktrees, archivedWorktrees } = useWorktreeStore.getState();
  return worktrees.find((w) => w.id === id) ?? archivedWorktrees.find((w) => w.id === id);
}

/** The project's main workspace: held, or fetched when the store lacks it. */
async function resolveMainWorktree(projectId: string): Promise<Worktree | null> {
  const held = useWorktreeStore.getState().worktrees.find((w) => w.is_main && w.project_id === projectId);
  if (held) return held;
  await fetchMainInto(projectId, true);
  return useWorktreeStore.getState().worktrees.find((w) => w.is_main && w.project_id === projectId) ?? null;
}

/**
 * Reload after a workspace moved between the live and archived lists. The
 * archived list is only reloaded when it has been loaded; otherwise the
 * archived view loads it fresh when opened.
 */
function reloadAfterArchiveChange(projectId: string): Promise<unknown> {
  const { hasLoadedArchived } = useWorktreeStore.getState();
  return Promise.all([
    useWorktreeStore.getState().refreshWorktrees(projectId),
    hasLoadedArchived ? useWorktreeStore.getState().loadArchivedWorktrees(projectId) : Promise.resolve(),
  ]);
}

/** Re-read one workspace by id and replace it wherever the store holds it. */
async function refreshWorktreeById(worktreeId: string): Promise<Worktree | null> {
  try {
    const fresh = grpcToStore(await worktreeGrpc.get(worktreeId));
    useWorktreeStore.setState((state) => ({
      worktrees: upsertWorktrees(state.worktrees, [fresh]),
      currentWorktree: refreshSelection(state.currentWorktree, [fresh]),
    }));
    return fresh;
  } catch (error) {
    logger.warn('[WorktreeStore] Could not refresh worktree by id', { worktreeId, error });
    return null;
  }
}

type ArchivedWorktreeActiveChatSnapshot = {
  activeChatId: string | null;
  activeChatWasInWorktree: boolean;
};

function getArchivedWorktreeActiveChatSnapshot(worktreeId: string): ArchivedWorktreeActiveChatSnapshot {
  const chatStore = useChatStore.getState();
  const activeChatId = chatStore.activeChatId;
  const activeChat = activeChatId ? getChatFromCache(activeChatId) : null;

  return {
    activeChatId,
    activeChatWasInWorktree: activeChat?.worktreeId === worktreeId,
  };
}

async function clearArchivedWorktreeActiveChat(
  worktreeId: string,
  snapshot: ArchivedWorktreeActiveChatSnapshot,
  options?: { clearMissingChat?: boolean }
): Promise<void> {
  const { activeChatId, activeChatWasInWorktree } = snapshot;
  if (!activeChatId) return;

  const chatStore = useChatStore.getState();
  if (chatStore.activeChatId !== activeChatId) return;

  const activeChat = getChatFromCache(activeChatId);
  const shouldClearActiveChat =
    activeChatWasInWorktree ||
    activeChat?.worktreeId === worktreeId ||
    (options?.clearMissingChat && !activeChat);

  if (!shouldClearActiveChat) return;

  logger.info('[WorktreeStore] Active chat was archived, clearing it', {
    activeChatId,
    worktreeId,
    activeChatStillLoaded: Boolean(activeChat),
  });

  chatStore.clearCurrentChat(worktreeId);
  await useChatNavigationStore.getState().removeFromQueue(activeChatId);
}

export const useWorktreeStore = create<WorktreeStore>((set, get) => ({
  worktrees: [],
  currentWorktree: null,
  discoveredWorktrees: [],
  isLoading: false,
  hasLoaded: false,
  nextPageToken: '',
  isLoadingMore: false,
  archivedWorktrees: [],
  archivedNextPageToken: '',
  hasLoadedArchived: false,
  isLoadingArchived: false,
  isDiscovering: false,
  staleWorktrees: [],
  workspacesRoot: '',
  deletingId: null,
  error: null,

  loadMainWorktree: async (projectId?: string) => {
    if (!projectId) return;
    await fetchMainInto(projectId, true);
  },

  loadWorktrees: async (projectId?: string) => {
    if (!projectId) {
      // gRPC requires projectId - return empty if not provided
      set({ worktrees: [], nextPageToken: '', isLoading: false, hasLoaded: true });
      return;
    }
    // Deduplicate concurrent calls for the same project (e.g. several pickers
    // mounting at once).
    await fetchFirstPageInto(projectId, true);
  },

  refreshWorktrees: async (projectId?: string) => {
    if (!projectId) return;
    await fetchFirstPageInto(projectId, false);
  },

  loadMoreWorktrees: async (projectId?: string) => {
    const { nextPageToken, isLoadingMore } = useWorktreeStore.getState();
    if (!projectId || !nextPageToken || isLoadingMore) return;
    const generation = listGeneration;
    set({ isLoadingMore: true });
    try {
      const page = await worktreeGrpc.list(projectId, { pageToken: nextPageToken });
      // The list was replaced while this page was in flight: it belongs to the
      // old list, whose token no longer continues anything.
      if (generation !== listGeneration) return;
      set((state) => ({
        worktrees: upsertWorktrees(state.worktrees, page.worktrees.map(grpcToStore)),
        nextPageToken: page.nextPageToken,
      }));
    } catch (error) {
      set({ error: getErrorMessage(error, 'Failed to load more workspaces') });
    } finally {
      if (generation === listGeneration) set({ isLoadingMore: false });
    }
  },

  loadArchivedWorktrees: async (projectId?: string) => {
    if (!projectId) {
      set({ archivedWorktrees: [], archivedNextPageToken: '', hasLoadedArchived: true });
      return;
    }
    const generation = ++archivedGeneration;
    set({ isLoadingArchived: true, error: null });
    try {
      const page = await singleflight(`loadArchivedWorktrees:${projectId}`, () => worktreeGrpc.listArchived(projectId));
      if (generation !== archivedGeneration) return;
      set({
        archivedWorktrees: page.worktrees.map(grpcToStore),
        archivedNextPageToken: page.nextPageToken,
        isLoadingArchived: false,
        hasLoadedArchived: true,
      });
    } catch (error) {
      if (generation !== archivedGeneration) return;
      set({
        error: getErrorMessage(error, 'Failed to load archived workspaces'),
        isLoadingArchived: false,
        hasLoadedArchived: true,
      });
    }
  },

  loadMoreArchivedWorktrees: async (projectId?: string) => {
    const { archivedNextPageToken, isLoadingArchived } = useWorktreeStore.getState();
    if (!projectId || !archivedNextPageToken || isLoadingArchived) return;
    const generation = archivedGeneration;
    set({ isLoadingArchived: true });
    try {
      const page = await worktreeGrpc.listArchived(projectId, { pageToken: archivedNextPageToken });
      if (generation !== archivedGeneration) return;
      set((state) => ({
        archivedWorktrees: upsertWorktrees(state.archivedWorktrees, page.worktrees.map(grpcToStore)),
        archivedNextPageToken: page.nextPageToken,
        isLoadingArchived: false,
      }));
    } catch (error) {
      if (generation !== archivedGeneration) return;
      set({ error: getErrorMessage(error, 'Failed to load more archived workspaces'), isLoadingArchived: false });
    }
  },

  // Reads through the creator's `get`, not useWorktreeStore.getState(): a value
  // returned from useWorktreeStore inside its own initializer makes the store's
  // type circular, and tsc then infers `any` for the whole store.
  resolveWorktree: async (worktreeId: string): Promise<Worktree | null> => {
    const held = get().worktrees.find((w) => w.id === worktreeId) ?? null;
    if (held) return held;
    if (unresolvableWorktreeIds.has(worktreeId)) return null;
    const inflight = resolvingWorktrees.get(worktreeId);
    if (inflight) return inflight;

    const request = (async (): Promise<Worktree | null> => {
      try {
        const fetched = grpcToStore(await worktreeGrpc.get(worktreeId));
        useWorktreeStore.setState((state) => ({
          worktrees: upsertWorktrees(state.worktrees, [fetched]),
        }));
        return fetched;
      } catch (error) {
        if (error instanceof ConnectError && (error.code === Code.NotFound || error.code === Code.PermissionDenied)) {
          unresolvableWorktreeIds.add(worktreeId);
        }
        logger.warn('[WorktreeStore] Could not resolve worktree by id', { worktreeId, error });
        return null;
      } finally {
        resolvingWorktrees.delete(worktreeId);
      }
    })();
    resolvingWorktrees.set(worktreeId, request);
    return request;
  },

  selectWorktree: (worktree: Worktree | null, options?: { skipWorkspaceStateSave?: boolean }) => {
    const previousWorktree = useWorktreeStore.getState().currentWorktree;
    
    // Don't do anything if selecting the same worktree
    if (previousWorktree?.id === worktree?.id) {
      return;
    }
    
    logger.info('[WorktreeStore] selectWorktree', {
      from: previousWorktree?.name || 'main',
      to: worktree?.name || 'main',
      skipWorkspaceStateSave: options?.skipWorkspaceStateSave,
    });
    
    set({ currentWorktree: worktree });
    
    // NOTE: viewerStore.currentWorktreeId removed - viewerStore now reads from worktreeStore.currentWorktree directly
    // This eliminates synchronization issues between stores
    
    // Save to workspace state for persistence (unless restoring)
    if (!options?.skipWorkspaceStateSave && worktree?.project_id) {
      useWorkspaceStateStore.getState().setLastWorktree(
        worktree.project_id,
        worktree.id
      );
    } else if (!options?.skipWorkspaceStateSave && previousWorktree?.project_id) {
      // Switching to main branch (null worktree)
      useWorkspaceStateStore.getState().setLastWorktree(
        previousWorktree.project_id,
        null
      );
    }
  },

  createWorktree: async (data: CreateWorktreeInput) => {
    set({ isLoading: true, error: null });
    try {
      const { project_id: projectId, name, branch } = data;
      if (!projectId || !name || !branch) {
        throw new Error('project_id, name, and branch are required');
      }

      // The server refuses to record a workspace on an asleep machine; it
      // wakes the machine instead, and this retries once it is up.
      const worktree = await retryAcrossWake(
        () =>
          worktreeGrpc.create(
            projectId,
            name,
            branch,
            {
              baseBranch: data.base_branch,
              baseBranches: data.base_branches,
              chatId: data.chat_id,
              copyFiles: data.copy_files,
              force: data.force,
              sourceWorktreeId: data.source_worktree_id,
            }
          ),
        { onWaking: announceMachineWaking },
      );

      const storeWorktree = grpcToStore(worktree);

      // Add to worktrees list
      set(state => ({
        worktrees: [...state.worktrees, storeWorktree],
        currentWorktree: storeWorktree,
        isLoading: false
      }));

      // The create settles in the background: say so now, and let the settle
      // (ACTIVE or FAILED) replace this toast. A create that comes back
      // already ACTIVE has nothing left to wait for.
      if (storeWorktree.status === WorktreeStatus.CREATING) {
        announceCreatePending(storeWorktree);
      } else if (storeWorktree.status === WorktreeStatus.ACTIVE) {
        announceCreated(storeWorktree);
      }

      return storeWorktree;
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to create workspace');
      set({
        error: errorMessage,
        isLoading: false
      });
      throw new Error(errorMessage);
    }
  },

  // ONLY archives a worktree (sets deleted_at). Never permanently deletes.
  // This is the function that should be used by the UI archive button.
  // Options control whether to cleanup local directory and/or git branch
  archiveWorktree: async (id: string, options?: { deleteGitBranch?: boolean }) => {
    set({ deletingId: id, error: null });
    try {
      // Get worktree info BEFORE archiving
      const worktree = findKnownWorktree(id);
      const worktreeName = worktree?.name || 'Workspace';
      const wasCurrentWorktree = useWorktreeStore.getState().currentWorktree?.id === id;
      const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

      await retryAcrossWake(
        () =>
          worktreeGrpc.archive(id, {
            deleteGitBranch: options?.deleteGitBranch,
          }),
        { onWaking: announceMachineWaking },
      );

      await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);

      // Reload worktrees to get updated state
      const currentProject = worktree?.project_id;
      if (currentProject) {
        await reloadAfterArchiveChange(currentProject);
        // Reload chats to remove archived ones from the list
        await useChatStore.getState().loadChats();

        await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });
        
        // Clean up workspace state for this worktree
        useWorkspaceStateStore.getState().removeWorktreeState(currentProject, id);
        
        // Clean up browser tabs for this worktree
        useBrowserStore.getState().closeWorktreeTabs(id);
        logger.info('[WorktreeStore] Cleaned up workspace state and browser tabs for archived worktree', { worktreeId: id });
        
        // If we archived the currently selected worktree, switch to main worktree
        if (wasCurrentWorktree) {
          const mainWorktree = await resolveMainWorktree(currentProject);
          if (mainWorktree) {
            logger.info('[WorktreeStore] Switching to main worktree after archiving current', {
              archivedWorktreeId: id,
              mainWorktreeId: mainWorktree.id,
              openFreshNewChat: true,
            });
            await useWorktreeStore.getState().switchWorktreeContext(currentProject, mainWorktree, { openFreshNewChat: true });
          } else {
            // No main worktree found, just clear selection
            set({ currentWorktree: null });
          }
        }
      }

      set({ deletingId: null });

      // Show toast with cleanup info
      const cleanupParts = [];
      if (options?.deleteGitBranch) cleanupParts.push('branch will be deleted once its files are removed');
      const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
      toast.success(`${worktreeName} archived${cleanupMsg}`, {
        duration: 4000,
      });
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to archive workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        deletingId: null
      });
    }
  },

  // Archives or permanently deletes a worktree based on its current state:
  // - If not archived (deleted_at is NULL): Archives it (sets deleted_at)
  // - If already archived (deleted_at is set): Permanently deletes from database
  // Options control whether to cleanup local directory and/or git branch
  deleteWorktree: async (id: string, options?: { deleteGitBranch?: boolean }) => {
    set({ deletingId: id, error: null });
    try {
      // Get worktree info BEFORE deleting
      const worktree = findKnownWorktree(id);
      const isPermanentDelete = worktree?.deleted_at !== null && worktree?.deleted_at !== undefined;
      const worktreeName = worktree?.name || 'Workspace';
      const wasCurrentWorktree = useWorktreeStore.getState().currentWorktree?.id === id;
      const currentProject = worktree?.project_id;
      const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

      await retryAcrossWake(
        () =>
          worktreeGrpc.delete(id, {
            deleteGitBranch: options?.deleteGitBranch,
          }),
        { onWaking: announceMachineWaking },
      );

      if (!isPermanentDelete) {
        await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);
      }

      set(state => ({
        // If permanent delete, remove from list; otherwise reload to get updated deleted_at
        worktrees: isPermanentDelete
          ? state.worktrees.filter(w => w.id !== id)
          : state.worktrees,
        archivedWorktrees: isPermanentDelete
          ? state.archivedWorktrees.filter(w => w.id !== id)
          : state.archivedWorktrees,
        // Don't clear currentWorktree here - we'll handle it below with proper switch to main
        deletingId: null
      }));

      // Show appropriate toast with cleanup info
      if (isPermanentDelete) {
        const cleanupParts = [];
        if (options?.deleteGitBranch) cleanupParts.push('branch will be deleted once its files are removed');
        const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
        toast.success(`${worktreeName} deleted permanently${cleanupMsg}`, {
          duration: 4000,
        });
      } else {
        const cleanupParts = [];
        if (options?.deleteGitBranch) cleanupParts.push('branch will be deleted once its files are removed');
        const cleanupMsg = cleanupParts.length > 0 ? ` (${cleanupParts.join(', ')})` : '';
        toast.success(`${worktreeName} archived${cleanupMsg}`, {
          duration: 4000,
        });
      }

      // Clean up workspace state and browser tabs for this worktree (for both archive and permanent delete)
      if (currentProject) {
        useWorkspaceStateStore.getState().removeWorktreeState(currentProject, id);
        useBrowserStore.getState().closeWorktreeTabs(id);
        logger.info('[WorktreeStore] Cleaned up workspace state and browser tabs for deleted worktree', { worktreeId: id, isPermanentDelete });
      }

      // If it was an archive (not permanent delete), reload worktrees and chats to get updated state
      if (!isPermanentDelete) {
        if (currentProject) {
          await reloadAfterArchiveChange(currentProject);
          // Reload chats to remove archived ones from the list
          await useChatStore.getState().loadChats();

          await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });
        }
      }

      // If we deleted/archived the currently selected worktree, switch to main worktree
      if (wasCurrentWorktree && currentProject) {
        const mainWorktree = await resolveMainWorktree(currentProject);
        if (mainWorktree) {
          const openFreshNewChat = !isPermanentDelete;
          logger.info('[WorktreeStore] Switching to main worktree after deleting current', {
            deletedWorktreeId: id,
            mainWorktreeId: mainWorktree.id,
            isPermanentDelete,
            openFreshNewChat,
          });
          await useWorktreeStore.getState().switchWorktreeContext(currentProject, mainWorktree, { openFreshNewChat });
        } else {
          // No main worktree found, just clear selection
          set({ currentWorktree: null });
        }
      }
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to delete workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        deletingId: null
      });
    }
  },

  unarchiveWorktree: async (id: string) => {
    set({ isLoading: true, error: null });
    try {
      const worktree = findKnownWorktree(id);
      const worktreeName = worktree?.name || 'Workspace';

      // A workspace whose files were removed has nothing to unarchive: rebuild
      // it from its branch, which also puts back any work saved at removal.
      let snapshotWarning = '';
      if (worktree?.cleanup_metadata?.directory_deleted) {
        snapshotWarning = (await worktreeGrpc.recreate(id)).snapshotWarning;
      } else {
        await worktreeGrpc.unarchive(id);
      }

      // Reload worktrees and chats to get updated state
      if (worktree?.project_id) {
        await reloadAfterArchiveChange(worktree.project_id);
        // Reload chats to bring back unarchived ones
        await useChatStore.getState().loadChats();
      }

      if (snapshotWarning) {
        toast.warning(`${worktreeName} restored, but its saved work was not applied. ${snapshotWarning}`, {
          duration: 15000,
        });
      } else {
        toast.success(`${worktreeName} restored`, {
          duration: 4000,
        });
      }

      set({ isLoading: false });
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to unarchive workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        isLoading: false
      });
      throw error;
    }
  },

  updateWorktreeStatus: async (id: string, status: Worktree['status']) => {
    set({ isLoading: true, error: null });
    try {
      await worktreeGrpc.update(id, { status });

      // Update in local state
      set(state => ({
        worktrees: state.worktrees.map(w =>
          w.id === id ? { ...w, status } : w
        ),
        currentWorktree: state.currentWorktree?.id === id
          ? { ...state.currentWorktree, status }
          : state.currentWorktree,
        isLoading: false
      }));
    } catch (error) {
      set({
        error: getErrorMessage(error, 'Failed to update workspace status'),
        isLoading: false
      });
    }
  },

  discoverWorktrees: async (projectId: string) => {
    set({ isDiscovering: true, error: null });
    try {
      const response = await worktreeGrpc.discover(projectId);
      set({
        discoveredWorktrees: response.discovered,
        staleWorktrees: response.stale,
        workspacesRoot: response.workspacesRoot,
        isDiscovering: false
      });
    } catch (error) {
      set({
        error: getErrorMessage(error, 'Failed to discover workspaces'),
        isDiscovering: false,
        discoveredWorktrees: [],
        staleWorktrees: [],
      });
    }
  },

  pruneWorktrees: async (projectId: string) => {
    const pruned = await worktreeGrpc.prune(projectId);
    const gone = new Set(pruned.map((w) => w.path));
    set((state) => ({
      staleWorktrees: state.staleWorktrees.filter((w) => !gone.has(w.path)),
    }));
    return pruned;
  },

  importWorktree: async (data) => {
    set({ isLoading: true, error: null });
    try {
      const worktree = await worktreeGrpc.import(data.project_id, data.path, {
        name: data.name,
        repoId: data.repo_id,
        confirmMove: data.confirm_move,
      });

      const storeWorktree = grpcToStore(worktree);

      // Add to worktrees list and mark as imported in discovered list
      set(state => ({
        worktrees: [...state.worktrees, storeWorktree],
        currentWorktree: storeWorktree,
        discoveredWorktrees: state.discoveredWorktrees.filter(w => w.path !== data.path),
        isLoading: false
      }));

      toast.success(`Workspace "${storeWorktree.name}" imported successfully`, {
        duration: 4000,
      });

      return storeWorktree;
    } catch (error) {
      const errorMessage = getErrorMessage(error, 'Failed to import workspace');
      toast.error(errorMessage);
      set({
        error: errorMessage,
        isLoading: false
      });
      throw new Error(errorMessage);
    }
  },

  /**
   * Restore the last selected worktree for a project from workspace state.
   * Called after worktrees are loaded for a project.
   * If no last worktree is saved, selects the main worktree by default.
   * @returns true if a worktree was restored/selected, false if no worktrees available
   */
  restoreLastWorktree: async (projectId: string) => {
    const workspaceState = useWorkspaceStateStore.getState();
    const lastWorktreeId = workspaceState.getProjectState(projectId).lastWorktreeId;

    // Select the project's main workspace as the fallback. Fetched on its own
    // when the store lacks it, so this works without the full list.
    const selectMainWorktree = async () => {
      const mainWorktree = await resolveMainWorktree(projectId);
      if (mainWorktree) {
        logger.info('[WorktreeStore] Selecting main worktree as default', {
          worktreeId: mainWorktree.id,
          name: mainWorktree.name,
          branch: mainWorktree.branch,
        });
        useWorktreeStore.getState().selectWorktree(mainWorktree, { skipWorkspaceStateSave: true });
        return true;
      }
      logger.warn('[WorktreeStore] No main worktree found for project', { projectId });
      set({ currentWorktree: null });
      return false;
    };

    if (!lastWorktreeId) {
      logger.info('[WorktreeStore] No last worktree to restore, selecting main worktree');
      return selectMainWorktree();
    }

    // By id: the saved workspace need not be in any loaded page. One that is
    // gone, archived, or another project's is not restorable.
    const worktree = await useWorktreeStore.getState().resolveWorktree(lastWorktreeId);
    if (!worktree || worktree.deleted_at || (worktree.project_id && worktree.project_id !== projectId)) {
      logger.warn('[WorktreeStore] Last worktree is not restorable, selecting main worktree', { lastWorktreeId });
      // Forget the saved workspace only when it is definitely unusable. A failed
      // lookup (offline, a blip) says nothing about it, and clearing the pointer
      // then would lose the user's place for good.
      if (worktree || unresolvableWorktreeIds.has(lastWorktreeId)) {
        workspaceState.setLastWorktree(projectId, null);
      }
      return selectMainWorktree();
    }

    logger.info('[WorktreeStore] Restoring last worktree', {
      worktreeId: worktree.id,
      name: worktree.name,
    });

    // Select without saving (we're restoring, not changing)
    useWorktreeStore.getState().selectWorktree(worktree, { skipWorkspaceStateSave: true });
    // Main stays held whichever workspace is selected: the terminal, browser
    // and chat header fall back to it. One row.
    await resolveMainWorktree(projectId);
    return true;
  },

  /**
   * Switch worktree context - saves current context and restores new context.
   * This triggers viewer store and chat navigation to switch contexts.
   */
  switchWorktreeContext: async (
    projectId: string,
    worktree: Worktree | null,
    options?: { openFreshNewChat?: boolean }
  ) => {
    const currentWorktree = useWorktreeStore.getState().currentWorktree;
    const newWorktreeId = worktree?.id ?? null;
    const currentWorktreeId = currentWorktree?.id ?? null;

    // Don't do anything if same worktree
    if (newWorktreeId === currentWorktreeId) {
      if (options?.openFreshNewChat) {
        useChatStore.getState().clearCurrentChat(newWorktreeId);
        useWorkspaceStateStore.getState().setActiveChatId(projectId, newWorktreeId, null);
      }
      return;
    }

    logger.info('[WorktreeStore] switchWorktreeContext', {
      from: currentWorktree?.name || 'main',
      to: worktree?.name || 'main',
      openFreshNewChat: options?.openFreshNewChat,
    });

    // Import viewerStore dynamically to avoid circular dependency
    // IMPORTANT: Must use sync import since we need this to complete before selectWorktree
    const { useViewerStore } = await import('./viewerStore');
    // Save current viewer state and switch context
    useViewerStore.getState().switchWorktreeContext(projectId, newWorktreeId);

    if (options?.openFreshNewChat) {
      useChatStore.getState().clearCurrentChat(newWorktreeId);
      useWorkspaceStateStore.getState().setActiveChatId(projectId, newWorktreeId, null);
    } else {
      // Restore chat navigation state for new worktree
      useChatNavigationStore.getState().restoreFromWorkspaceState(projectId, newWorktreeId);
    }

    // Select the worktree (this saves to workspace state)
    useWorktreeStore.getState().selectWorktree(worktree);
  },

  reset: () => {
    // Any in-flight load predates this reset, so let it be dropped on arrival.
    lastAppliedTicket = loadTicket;
    announcedCreateFailures.clear();
    announcedCreates.clear();
    unresolvableWorktreeIds.clear();
    resolvingWorktrees.clear();
    listGeneration++;
    archivedGeneration++;
    set({
      worktrees: [],
      currentWorktree: null,
      discoveredWorktrees: [],
      staleWorktrees: [],
      workspacesRoot: '',
      isLoading: false,
      hasLoaded: false,
      nextPageToken: '',
      isLoadingMore: false,
      archivedWorktrees: [],
      archivedNextPageToken: '',
      hasLoadedArchived: false,
      isLoadingArchived: false,
      isDiscovering: false,
      deletingId: null,
      error: null,
    });
  },
}));

const unsubscribeWorktreeRefetch = subscribeToRefetch('worktree_changes', handleWorktreeRefetch);
// A hot reload re-evaluates this module; drop the old listener so settles are
// not handled twice.
import.meta.hot?.dispose(unsubscribeWorktreeRefetch);

/**
 * Get the active worktree ID for global workspace context (terminal, file browser, search, etc).
 *
 * This is the single source of truth for "which workspace are we working in".
 * Falls back to the main worktree if no worktree is explicitly selected.
 *
 * NOTE: Do NOT use this for chat-specific context (e.g. rendering file links in messages).
 * For that, use the chat's own worktreeId.
 */
export function useActiveWorktreeId(): string | undefined {
  const currentWorktreeId = useWorktreeStore((state) => state.currentWorktree?.id);
  const mainWorktreeId = useWorktreeStore((state) => state.worktrees.find((w) => w.is_main)?.id);
  return currentWorktreeId || mainWorktreeId;
}