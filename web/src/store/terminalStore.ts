import { create } from "zustand";
import { persist } from "zustand/middleware";
import { logger } from "../lib/logger";
import { terminalApi } from "../api/terminal-grpc";
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
   */
  daemonSessionId?: string;
}

interface TerminalState {
  sessions: TerminalSession[];
  activeSessionId: string | null;
  activeSessionPerWorktree: Record<string, string>; // worktreeId -> sessionId ("__main__" for main workspace)
  isOpen: boolean;

  // Actions
  createSession: (workingDir?: string, projectId?: string, worktreeId?: string) => string;
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
  setDaemonSessionId: (id: string, daemonSessionId: string) => void;
  toggleTerminal: () => void; // Show/hide the terminal panel
  showTerminal: () => void; // Expand/show the terminal panel
  hideTerminal: () => void; // Collapse/hide the terminal panel

  closeSession: (id: string) => void;
  openTerminal: () => void;
  closeTerminal: () => void;
}

// Define what we want to persist
type PersistedTerminalState = Pick<TerminalState, 'sessions' | 'activeSessionId' | 'activeSessionPerWorktree'>;

// Key for main workspace (no worktree)
const MAIN_WORKSPACE_KEY = '__main__';

export const useTerminalStore = create(
  persist<TerminalState, [], [], PersistedTerminalState>(
    (set, get) => ({
  sessions: [],
  activeSessionId: null,
  activeSessionPerWorktree: {},
  isOpen: false,

  createSession: (workingDir?: string, projectId?: string, worktreeId?: string) => {
    const id = `terminal-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
    const worktreeKey = worktreeId || MAIN_WORKSPACE_KEY;
    const session: TerminalSession = {
      id,
      title: `Terminal ${get().sessions.length + 1}`,
      workingDir,
      projectId,
      worktreeId,
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
    }));

    logger.info("[TerminalStore] Created session", { id, workingDir, projectId, worktreeId });
    return id;
  },

  killSession: (id: string) => {
    // Close on the server with the DAEMON's id, not `id` — `id` is local and
    // the daemon has never seen it. Sending it failed every close with
    // "session not found" and left the PTY running in the workspace until the
    // pod died. Fire-and-forget: the UI drops the tab immediately.
    const daemonSessionId = get().sessions.find((s) => s.id === id)?.daemonSessionId;
    if (daemonSessionId) {
      terminalApi.closeSession(daemonSessionId).catch((error) => {
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

  setDaemonSessionId: (id: string, daemonSessionId: string) => {
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === id ? { ...s, daemonSessionId } : s
      ),
    }));
    logger.debug("[TerminalStore] Bound daemon session id", { id, daemonSessionId });
  },

  updateSessionPID: (id: string, pid: number) => {
    set((state) => ({
      sessions: state.sessions.map((s) =>
        s.id === id ? { ...s, pid } : s
      ),
    }));
    logger.debug("[TerminalStore] Updated session PID", { id, pid });
  },

  toggleTerminal: () => {
    const newIsOpen = !get().isOpen;
    set({ isOpen: newIsOpen });
    logger.debug("[TerminalStore] Toggled terminal panel", { isOpen: newIsOpen });
    
    // Persist to workspace state
    // Import dynamically to avoid circular dependency
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, newIsOpen);
        }
      });
    });
  },

  showTerminal: () => {
    set({ isOpen: true });
    logger.debug("[TerminalStore] Showed terminal panel");
    
    // Persist to workspace state
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, true);
        }
      });
    });
  },

  hideTerminal: () => {
    set({ isOpen: false });
    logger.debug("[TerminalStore] Hid terminal panel");
    
    // Persist to workspace state
    import("./projectStore").then(({ useProjectStore }) => {
      import("./worktreeStore").then(({ useWorktreeStore }) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
        if (projectId) {
          useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, false);
        }
      });
    });
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
        sessions: state.sessions,
        activeSessionId: state.activeSessionId,
        activeSessionPerWorktree: state.activeSessionPerWorktree,
      }),
      // Rehydrate
      onRehydrateStorage: () => (state) => {
        if (state?.sessions) {
          // Convert createdAt strings back to Date objects after rehydration
          state.sessions = state.sessions.map(session => ({
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
