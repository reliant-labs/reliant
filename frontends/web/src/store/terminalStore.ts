import { create } from "zustand";
import { persist } from "zustand/middleware";
import { logger } from "../lib/logger";
import { TerminalService } from "../gen/reliant/v1/terminal_pb";
import { terminalServiceKeys } from "../hooks/terminal-service-hooks_gen";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import { useWorkspaceStateStore } from "./workspaceStateStore";

export interface TerminalSession {
  id: string;
  title: string;
  workingDir?: string;
  projectId?: string; // Track which project this terminal belongs to
  worktreeId?: string; // Track which worktree this terminal belongs to
  createdAt: Date;
  isActive: boolean;
  pid?: number;
  /**
   * The DAEMON's session id, learned from the server's "init" message.
   *
   * `id` above is minted locally before any socket exists, because the React
   * tree needs a stable key immediately. It is meaningless to the daemon. Only
   * this id can address the real PTY, so it is what CloseSession must send —
   * passing `id` made every close fail with "session not found" and left the
   * shell process running in the workspace.
   *
   * Undefined until "init" arrives, and undefined forever for a session whose
   * socket never connected. Closing one of those is correctly a no-op.
   *
   * Connection-scoped, so never persisted: the server closes the daemon
   * session when its socket drops, and an id restored after a reload names a
   * session that no longer exists (ELECTRON-97).
   */
  daemonSessionId?: string;
  /**
   * The machine the daemon session runs on, from the same "init" (absent when
   * the server left it to default resolution). A session lives on one
   * machine, so CloseSession is sent there. Connection-scoped like
   * daemonSessionId.
   */
  daemonId?: string;
  /**
   * The chat open when the terminal was created. On the main checkout, which
   * every machine with the project has, the terminal opens on that chat's
   * machine — where its agent is changing the files.
   */
  chatId?: string;
}

/**
 * The user asked for the cursor to be in the terminal.
 *
 * The terminal used to focus itself whenever a session became active, and
 * sessions become active without anyone asking: a new workspace restores its
 * panel open, the panel creates the session when the checkout lands ~10s
 * later, and by then the user is typing in the composer — the terminal took
 * the cursor mid-sentence. Switching workspaces did the same.
 *
 * So focus follows intent only. The user-facing actions below (opening or
 * toggling the panel, creating a terminal, picking a tab, the focus-terminal
 * shortcut) make a request; restoring saved panel state and the session the
 * panel creates for itself make none. The visible terminal claims a request as
 * soon as it can take focus (claimFocusRequest), so a request made before the
 * terminal exists waits for it, but it is withdrawn the moment the user does
 * something elsewhere — a key, a pointer, or text by any route
 * (armFocusRequestExpiry), or when the panel stops showing the workspace it
 * was made in (withdrawFocusRequest): a terminal that turns up after the user
 * moved on must not pull them back.
 */
export interface TerminalFocusRequest {
  id: number;
  /** What had focus when the user asked. A user still waiting has left it there. */
  restingFocus: Element | null;
}

/**
 * Selector for the terminal panel's root, which carries `data-terminal-panel`.
 * Activity inside it is the user engaging with the terminal, so it never
 * withdraws a focus request, and focus inside it is the terminal's to move.
 */
const TERMINAL_PANEL_SELECTOR = "[data-terminal-panel]";

interface TerminalState {
  sessions: TerminalSession[];
  activeSessionId: string | null;
  activeSessionPerWorktree: Record<string, string>; // worktreeId -> sessionId ("__main__" for main workspace)
  isOpen: boolean;
  /** Pending request for the terminal to take focus. See TerminalFocusRequest. */
  focusRequest: TerminalFocusRequest | null;

  // Actions
  /**
   * Create a session and make it the active one. Focuses it, because creating
   * a terminal is something the user asks for — except `focus: false`, for a
   * session the app creates on its own.
   */
  createSession: (
    workingDir?: string,
    projectId?: string,
    worktreeId?: string,
    options?: { focus?: boolean; chatId?: string },
  ) => string;
  killSession: (id: string) => void; // Terminates the session process
  setActiveSession: (id: string) => void;
  setActiveSessionForWorktree: (worktreeId: string | undefined, sessionId: string) => void;
  getActiveSession: () => TerminalSession | null;
  getActiveSessionForWorktree: (worktreeId: string | undefined) => string | null;
  getProjectSessions: (projectId?: string) => TerminalSession[];
  getWorktreeSessions: (worktreeId?: string) => TerminalSession[];
  updateSessionTitle: (id: string, title: string) => void;
  updateSessionPID: (id: string, pid: number) => void;
  /** Bind the daemon's session id once "init" arrives. See TerminalSession.daemonSessionId. */
  setDaemonSessionId: (id: string, daemonSessionId: string, daemonId?: string) => void;
  /**
   * Drop the project's sessions whose workspace is gone (archived, deleted,
   * or no longer listed). See pruneSessionsForWorkspaces.
   */
  pruneSessions: (projectId: string, liveWorktreeIds: ReadonlySet<string>) => void;
  // The panel actions below are the USER's: opening requests focus, and the
  // new state is saved for the workspace. Restoring saved state is restoreOpen.
  toggleTerminal: () => void; // Show/hide the terminal panel
  showTerminal: () => void; // Expand/show the terminal panel
  hideTerminal: () => void; // Collapse/hide the terminal panel
  /**
   * Match the panel to the workspace's saved state. Not a user action, so it
   * never moves focus and saves nothing (the value came from the save).
   */
  restoreOpen: (open: boolean) => void;
  /** Ask for the cursor in the active terminal, now or as soon as it exists. User intent only. */
  requestFocus: () => void;
  /**
   * Drop the pending request: the panel no longer shows the workspace it was
   * made in, and another workspace's terminal must never claim it.
   */
  withdrawFocusRequest: () => void;
  /**
   * The visible terminal settles request `id`: clears it and returns whether
   * to take focus — false when it is stale, or focus has since gone somewhere
   * other than where the user left it.
   */
  claimFocusRequest: (id: number) => boolean;

  closeSession: (id: string) => void;
  openTerminal: () => void;
  closeTerminal: () => void;
}

// Define what we want to persist
type PersistedTerminalState = Pick<TerminalState, 'sessions' | 'activeSessionId' | 'activeSessionPerWorktree'>;

// Key for main workspace (no worktree)
const MAIN_WORKSPACE_KEY = '__main__';

/**
 * The sessions to keep once a project's live workspaces are known.
 *
 * Sessions persist across reloads, and every one in the project mounts a
 * terminal with its own WebSocket, visible or not. Nothing removed the
 * session of a workspace that was archived or deleted, so its hidden terminal
 * reconnected to a directory that no longer existed — every ~12s, for five
 * hours, in prod (2026-10-08). A session with no worktree id belongs to the
 * project's main checkout and is always kept.
 */
export function pruneSessionsForWorkspaces(
  sessions: readonly TerminalSession[],
  projectId: string,
  liveWorktreeIds: ReadonlySet<string>,
): TerminalSession[] {
  return sessions.filter(
    (s) => s.projectId !== projectId || !s.worktreeId || liveWorktreeIds.has(s.worktreeId),
  );
}

/** Sessions as they are written to storage: no connection-scoped fields. */
function persistableSessions(sessions: readonly TerminalSession[]): TerminalSession[] {
  return sessions.map(({ daemonSessionId: _daemonSessionId, daemonId: _daemonId, ...rest }) => rest);
}

function isInsideTerminalPanel(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest(TERMINAL_PANEL_SELECTOR) !== null;
}

/**
 * The events that mean the user is doing something: a key, a pointer, or text
 * arriving by any route.
 *
 * beforeinput is not redundant with keydown. Text reaches the composer with no
 * keydown at all — dictation, an IME commit, paste from the context menu, a
 * drop — and a request made from the composer cannot use focus to tell "still
 * waiting" from "writing again", because focus never left the element it
 * remembers (restingFocus). Missing that text left the request standing, and
 * the terminal took the cursor mid-sentence when it connected.
 */
const USER_ACTIVITY_EVENTS = ["keydown", "pointerdown", "beforeinput"] as const;

/**
 * Call `withdraw` the moment the user does something outside the terminal
 * panel (USER_ACTIVITY_EVENTS). Returns the disarm function.
 *
 * Armed on the next task, not now: a request is made from inside the very
 * keydown or click that expressed it, and that event is the intent, not the
 * user moving on.
 */
function armFocusRequestExpiry(withdraw: () => void): () => void {
  if (typeof document === "undefined") return () => {};
  const onActivity = (event: Event) => {
    if (!isInsideTerminalPanel(event.target)) withdraw();
  };
  let armed = false;
  const timer = setTimeout(() => {
    armed = true;
    for (const type of USER_ACTIVITY_EVENTS) document.addEventListener(type, onActivity, true);
  }, 0);
  return () => {
    clearTimeout(timer);
    if (!armed) return;
    for (const type of USER_ACTIVITY_EVENTS) document.removeEventListener(type, onActivity, true);
  };
}

// At most one request is pending; these are its expiry listeners.
let lastFocusRequestId = 0;
let disarmPendingFocusRequest: () => void = () => {};

/** A fresh request, superseding any pending one. Goes into the same set() as the action that made it. */
function newFocusRequest(): TerminalFocusRequest {
  disarmPendingFocusRequest();
  const request: TerminalFocusRequest = {
    id: ++lastFocusRequestId,
    restingFocus: typeof document === "undefined" ? null : document.activeElement,
  };
  disarmPendingFocusRequest = armFocusRequestExpiry(() => {
    if (useTerminalStore.getState().focusRequest?.id === request.id) {
      useTerminalStore.setState({ focusRequest: dropFocusRequest() });
    }
  });
  return request;
}

/** Withdraw the pending request. Returns the `focusRequest` value to set. */
function dropFocusRequest(): null {
  disarmPendingFocusRequest();
  disarmPendingFocusRequest = () => {};
  return null;
}

/**
 * Whether focus is still where a user waiting for the terminal would leave it:
 * nowhere, on whatever had it when they asked, or already in the panel.
 */
function focusIsWhereUserLeftIt(request: TerminalFocusRequest): boolean {
  const active = document.activeElement;
  return (
    !active ||
    active === document.body ||
    active === request.restingFocus ||
    isInsideTerminalPanel(active)
  );
}

export const useTerminalStore = create(
  persist<TerminalState, [], [], PersistedTerminalState>(
    (set, get) => ({
  sessions: [],
  activeSessionId: null,
  activeSessionPerWorktree: {},
  isOpen: false,
  focusRequest: null,

  createSession: (workingDir?: string, projectId?: string, worktreeId?: string, options?: { focus?: boolean; chatId?: string }) => {
    const id = `terminal-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
    const worktreeKey = worktreeId || MAIN_WORKSPACE_KEY;
    const session: TerminalSession = {
      id,
      title: `Terminal ${get().sessions.length + 1}`,
      workingDir,
      projectId,
      worktreeId,
      chatId: options?.chatId,
      createdAt: new Date(),
      isActive: true,
    };

    set((state) => ({
      sessions: [...state.sessions, session],
      activeSessionId: id,
      activeSessionPerWorktree: {
        ...state.activeSessionPerWorktree,
        [worktreeKey]: id,
      },
      ...(options?.focus === false ? {} : { focusRequest: newFocusRequest() }),
    }));

    logger.info("[TerminalStore] Created session", { id, workingDir, projectId, worktreeId });
    return id;
  },

  killSession: (id: string) => {
    // Close on the server with the DAEMON's id, not `id` — `id` is local and
    // the daemon has never seen it. Sending it failed every close with
    // "session not found" and left the PTY running in the workspace until the
    // pod died. Fire-and-forget: the UI drops the tab immediately.
    const closing = get().sessions.find((s) => s.id === id);
    const daemonSessionId = closing?.daemonSessionId;
    if (daemonSessionId) {
      // A store action, not a component, so it calls the client the
      // generated useCloseSession wraps and applies the same invalidation.
      connectClient(TerminalService)
        .closeSession({ sessionId: daemonSessionId, daemonId: closing?.daemonId })
        .then((response) => {
          void queryClient.invalidateQueries({ queryKey: terminalServiceKeys.all });
          if (!response.success) {
            logger.warn("[TerminalStore] Close session failed", { id, daemonSessionId, message: response.message });
          }
        })
        .catch((error) => {
          // Still best-effort: the session may have exited on its own (the shell
          // was exited, or the daemon restarted) and is genuinely gone already.
          logger.warn("[TerminalStore] Failed to close session on server (may already be closed)", { id, daemonSessionId, error });
        });
    } else {
      // No daemon id means "init" never arrived, so no PTY was ever created
      // for this tab. There is nothing to close, and calling CloseSession with
      // the local id is what produced the errors this replaces.
      logger.debug("[TerminalStore] No daemon session to close", { id });
    }

    set((state) => {
      const sessions = state.sessions.filter((s) => s.id !== id);
      let activeSessionId = state.activeSessionId;

      // If killing active session, switch to another
      if (activeSessionId === id) {
        activeSessionId = sessions.length > 0 ? sessions[sessions.length - 1].id : null;
      }

      return { sessions, activeSessionId };
    });

    logger.info("[TerminalStore] Killed session", { id });
  },

  setActiveSession: (id: string) => {
    const session = get().sessions.find((s) => s.id === id);
    if (session) {
      // Also update the worktree-specific active session
      const worktreeKey = session.worktreeId || MAIN_WORKSPACE_KEY;
      set((state) => ({
        activeSessionId: id,
        activeSessionPerWorktree: {
          ...state.activeSessionPerWorktree,
          [worktreeKey]: id,
        },
      }));
    } else {
      set({ activeSessionId: id });
    }
    logger.debug("[TerminalStore] Set active session", { id });
  },

  setActiveSessionForWorktree: (worktreeId: string | undefined, sessionId: string) => {
    const worktreeKey = worktreeId || MAIN_WORKSPACE_KEY;
    set((state) => ({
      activeSessionId: sessionId,
      activeSessionPerWorktree: {
        ...state.activeSessionPerWorktree,
        [worktreeKey]: sessionId,
      },
    }));
    logger.debug("[TerminalStore] Set active session for worktree", { worktreeId, sessionId });
  },

  getActiveSession: () => {
    const state = get();
    if (!state.activeSessionId) return null;
    return state.sessions.find((s) => s.id === state.activeSessionId) || null;
  },

  getActiveSessionForWorktree: (worktreeId: string | undefined) => {
    const state = get();
    const worktreeKey = worktreeId || MAIN_WORKSPACE_KEY;
    return state.activeSessionPerWorktree[worktreeKey] || null;
  },

  getProjectSessions: (projectId?: string) => {
    const state = get();
    // Return all sessions for this project
    return state.sessions.filter((s) => s.projectId === projectId);
  },

  getWorktreeSessions: (worktreeId?: string) => {
    const state = get();
    // Return all sessions for this worktree
    return state.sessions.filter((s) => s.worktreeId === worktreeId);
  },

  updateSessionTitle: (id: string, title: string) => {
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === id ? { ...s, title } : s
      ),
    }));
  },

  setDaemonSessionId: (id: string, daemonSessionId: string, daemonId?: string) => {
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === id ? { ...s, daemonSessionId, daemonId } : s
      ),
    }));
    logger.debug("[TerminalStore] Bound daemon session id", { id, daemonSessionId, daemonId });
  },

  updateSessionPID: (id: string, pid: number) => {
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === id ? { ...s, pid } : s
      ),
    }));
    logger.debug("[TerminalStore] Updated session PID", { id, pid });
  },

  pruneSessions: (projectId: string, liveWorktreeIds: ReadonlySet<string>) => {
    const before = get().sessions;
    const sessions = pruneSessionsForWorkspaces(before, projectId, liveWorktreeIds);
    if (sessions.length === before.length) return;
    set((state) => {
      const kept = new Set(sessions.map((s) => s.id));
      const activeSessionPerWorktree = Object.fromEntries(
        Object.entries(state.activeSessionPerWorktree).filter(([, id]) => kept.has(id)),
      );
      const activeSessionId =
        state.activeSessionId && kept.has(state.activeSessionId) ? state.activeSessionId : null;
      return { sessions, activeSessionPerWorktree, activeSessionId };
    });
    logger.info("[TerminalStore] Pruned sessions of removed workspaces", {
      projectId,
      removed: before.length - sessions.length,
    });
  },

  toggleTerminal: () => {
    const newIsOpen = !get().isOpen;
    set({ isOpen: newIsOpen, focusRequest: newIsOpen ? newFocusRequest() : dropFocusRequest() });
    logger.debug("[TerminalStore] Toggled terminal panel", { isOpen: newIsOpen });
    
    // Persist to workspace state
    // Import dynamically to avoid circular dependency
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, newIsOpen);
        }
      });
    });
  },

  showTerminal: () => {
    set({ isOpen: true, focusRequest: newFocusRequest() });
    logger.debug("[TerminalStore] Showed terminal panel");
    
    // Persist to workspace state
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, true);
        }
      });
    });
  },

  hideTerminal: () => {
    set({ isOpen: false, focusRequest: dropFocusRequest() });
    logger.debug("[TerminalStore] Hid terminal panel");
    
    // Persist to workspace state
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, false);
        }
      });
    });
  },

  restoreOpen: (open: boolean) => {
    if (get().isOpen === open) return;
    set(open ? { isOpen: true } : { isOpen: false, focusRequest: dropFocusRequest() });
    logger.debug("[TerminalStore] Restored terminal panel state", { isOpen: open });
  },

  requestFocus: () => {
    set({ focusRequest: newFocusRequest() });
  },

  withdrawFocusRequest: () => {
    if (get().focusRequest) set({ focusRequest: dropFocusRequest() });
  },

  claimFocusRequest: (id: number) => {
    const request = get().focusRequest;
    if (!request || request.id !== id) return false;
    set({ focusRequest: dropFocusRequest() });
    return focusIsWhereUserLeftIt(request);
  },

  closeSession: (id: string) => get().killSession(id),
  openTerminal: () => get().showTerminal(),
  closeTerminal: () => get().hideTerminal(),
    }),
    {
      name: 'terminal-store',
      version: 2,
      // Only persist session metadata, not the isOpen state
      partialize: (state): PersistedTerminalState => ({
        sessions: persistableSessions(state.sessions),
        activeSessionId: state.activeSessionId,
        activeSessionPerWorktree: state.activeSessionPerWorktree,
      }),
      // Rehydrate
      onRehydrateStorage: () => (state) => {
        if (state?.sessions) {
          // Convert createdAt strings back to Date objects after rehydration
          // persistableSessions also drops daemonSessionId from anything
          // stored before it stopped being persisted.
          state.sessions = persistableSessions(state.sessions).map(session => ({
            ...session,
            createdAt: session.createdAt instanceof Date
              ? session.createdAt
              : new Date(session.createdAt as string | number)
          }));
          logger.info('[TerminalStore] Rehydrated sessions', {
            count: state.sessions.length,
            sessionIds: state.sessions.map(s => s.id)
          });
        }
      },
    }
  )
);
