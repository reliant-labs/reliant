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
    // Phase 12: Close the session on the server via gRPC
    // Fire-and-forget - we update UI immediately for responsiveness
    terminalApi.closeSession(id).catch((error) => {
      // Log but don't block - session might have already been closed server-side
      logger.warn("[TerminalStore] Failed to close session on server (may already be closed)", { id, error });
    });

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
