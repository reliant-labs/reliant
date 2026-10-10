import { useCallback, useRef, useEffect, useState, useMemo } from "react";
import {
  Plus,
  Trash2,
  ChevronDown,
  Terminal as TerminalIcon,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { Terminal } from "./Terminal";
import { useTerminalStore } from "../../store/terminalStore";
import { useProjectStore } from "../../store/projectStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { useWorktreeStore, useActiveWorktreeId } from "../../store/worktreeStore";
import { resolveTerminalWorkingDir } from "../../lib/terminalWorkingDir";
import { Tooltip } from "../ui/Tooltip";
import { logger } from "../../lib/logger";

interface TerminalPanelProps {
  hasViewer?: boolean; // Renamed from hasDiffViewer to hasViewer (includes both file viewer and diff viewer)
}

/**
 * Terminal panel - Positioned within flex layout
 * Shares width with viewer panel and manages height via resize handle
 * Terminals are scoped to the current chat's workspace/worktree
 */
export function TerminalPanel(_props: TerminalPanelProps) {
  const [isResizingWidth, setIsResizingWidth] = useState(false);
  const setIsResizingGlobal = useSidebarStore((state) => state.setIsResizing);
  const panelRef = useRef<HTMLDivElement>(null);

  const getWorktreeSessions = useTerminalStore(
    (state) => state.getWorktreeSessions
  );
  // Get ALL sessions to keep them mounted (prevents process killing on workspace switch)
  const allSessions = useTerminalStore((state) => state.sessions);
  const activeSessionId = useTerminalStore((state) => state.activeSessionId);
  const getActiveSessionForWorktree = useTerminalStore(
    (state) => state.getActiveSessionForWorktree
  );
  const setActiveSessionForWorktree = useTerminalStore(
    (state) => state.setActiveSessionForWorktree
  );
  const createSession = useTerminalStore((state) => state.createSession);
  const killSession = useTerminalStore((state) => state.killSession);
  const setActiveSession = useTerminalStore((state) => state.setActiveSession);
  const hideTerminal = useTerminalStore((state) => state.hideTerminal);
  const isTerminalOpen = useTerminalStore((state) => state.isOpen);
  const currentProject = useProjectStore((state) => state.currentProject);

  // Use the global active worktree (from worktreeStore) as the single source of truth
  const currentWorktreeId = useActiveWorktreeId();
  
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const selectedWorktree = useWorktreeStore((state) => state.currentWorktree);

  // Where a terminal for the active workspace starts. A workspace still being
  // created has no directory yet; it resolves to "pending" (never the project
  // root) and the effect below creates the session once the path settles.
  const workingDir = useMemo(() => {
    if (!currentWorktreeId) return resolveTerminalWorkingDir(null, currentProject);
    const worktree =
      worktrees.find((w) => w.id === currentWorktreeId) ??
      (selectedWorktree?.id === currentWorktreeId ? selectedWorktree : undefined);
    // An id we know nothing about yet is a workspace whose row has not reached
    // the store — pending, not "no workspace".
    if (!worktree) return { kind: "pending" } as const;
    return resolveTerminalWorkingDir(worktree, currentProject);
  }, [currentWorktreeId, worktrees, selectedWorktree, currentProject]);

  // Drop terminals whose workspace is gone. Every project session below
  // mounts a terminal with its own socket, hidden or not, so a stale one is
  // not just clutter — it reconnects forever to a directory that will never
  // come back. Only once the list for THIS project has loaded: the main
  // worktree is always in it, so its absence means the list is someone
  // else's or not here yet, and pruning against it would drop live sessions.
  const worktreesLoaded = useWorktreeStore((state) => state.hasLoaded);
  const pruneSessions = useTerminalStore((state) => state.pruneSessions);
  useEffect(() => {
    const projectId = currentProject?.id;
    if (!projectId || !worktreesLoaded) return;
    const projectWorktrees = worktrees.filter((w) => w.project_id === projectId);
    if (!projectWorktrees.some((w) => w.is_main)) return;
    pruneSessions(
      projectId,
      new Set(projectWorktrees.filter((w) => !w.deleted_at).map((w) => w.id)),
    );
  }, [currentProject?.id, worktreesLoaded, worktrees, pruneSessions]);

  // Get sessions scoped to current worktree (for UI display)
  const sessions = getWorktreeSessions(currentWorktreeId);

  // Keep terminals alive across workspace switches, but never mount a
  // persisted terminal for a workspace that is no longer in the active list.
  // A mounted terminal reconnects on a missing directory by design; retaining
  // one after its workspace disappears turns that recovery loop into permanent
  // daemon work.
  const projectSessions = useMemo(() => {
    const activeWorktreeIDs = new Set(
      worktrees.filter((worktree) => !worktree.deleted_at).map((worktree) => worktree.id)
    );
    return allSessions.filter(
      (session) =>
        session.projectId === currentProject?.id &&
        (!session.worktreeId || activeWorktreeIDs.has(session.worktreeId))
    );
  }, [allSessions, currentProject?.id, worktrees]);

  // Get real-time dimensions from shared store
  const sidebarWidth = useSidebarStore((state) => state.width);
  const setWidth = useSidebarStore((state) => state.setWidth);

  // When worktree changes or terminal opens, switch to that worktree's active terminal or create one
  useEffect(() => {
    if (!currentProject || !isTerminalOpen) return;

    // Check if this worktree has sessions
    const worktreeSessions = getWorktreeSessions(currentWorktreeId);

    if (worktreeSessions.length > 0) {
      // Worktree has sessions - restore the last active one
      const lastActiveSessionId = getActiveSessionForWorktree(currentWorktreeId);
      const sessionExists = worktreeSessions.find((s) => s.id === lastActiveSessionId);

      if (sessionExists) {
        // Restore the last active session for this worktree
        setActiveSession(lastActiveSessionId!);
      } else {
        // Last active session no longer exists, use first available
        setActiveSession(worktreeSessions[0].id);
      }
    } else if (workingDir.kind === "ready") {
      // No sessions for this worktree - create one. A pending workspace
      // re-runs this effect when its path lands.
      const sessionId = createSession(workingDir.path, currentProject.id, currentWorktreeId);
      logger.info("[TerminalPanel] Created session for worktree", {
        worktreeId: currentWorktreeId,
        sessionId,
        workingDir: workingDir.path,
      });
    }
  }, [currentWorktreeId, currentProject, workingDir, getWorktreeSessions, getActiveSessionForWorktree, setActiveSession, createSession, isTerminalOpen]);

  const handleNewTerminal = useCallback(() => {
    if (!currentProject || workingDir.kind !== "ready") return;
    const sessionId = createSession(workingDir.path, currentProject.id, currentWorktreeId);
    logger.info("[TerminalPanel] Created new terminal", {
      worktreeId: currentWorktreeId,
      sessionId,
      workingDir: workingDir.path,
    });
  }, [currentProject, workingDir, createSession, currentWorktreeId]);

  const handleKillTerminal = useCallback(
    (sessionId: string, e: React.MouseEvent) => {
      e.stopPropagation();
      killSession(sessionId);

      // If this was the last session for this worktree, hide the panel
      if (sessions.length === 1) {
        hideTerminal();
      }
    },
    [killSession, hideTerminal, sessions.length]
  );

  // Handler to set active session with worktree tracking
  const handleSetActiveSession = useCallback(
    (sessionId: string) => {
      setActiveSessionForWorktree(currentWorktreeId, sessionId);
    },
    [currentWorktreeId, setActiveSessionForWorktree]
  );

  // Dispatch event when terminal opens or active session changes to trigger refit and focus
  useEffect(() => {
    if (!isTerminalOpen) return;
    
    // Terminal is now visible, dispatch event to trigger fit
    window.dispatchEvent(new CustomEvent("terminal-container-changed"));

    // Focus the active terminal session
    if (activeSessionId) {
      window.dispatchEvent(
        new CustomEvent("focus-active-terminal", {
          detail: { sessionId: activeSessionId },
        })
      );
    }
  }, [activeSessionId, isTerminalOpen]);

  // Handle width resizing
  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isResizingWidth || !panelRef.current) return;

      const containerRight = panelRef.current.getBoundingClientRect().right;
      const newWidth = containerRight - e.clientX;

      const minWidth = 300;
      const maxWidth = 1200;

      if (newWidth >= minWidth && newWidth <= maxWidth) {
        setWidth(newWidth);
      }
    };

    const handleMouseUp = () => {
      setIsResizingWidth(false);
      setIsResizingGlobal(false);
    };

    if (isResizingWidth) {
      document.addEventListener("mousemove", handleMouseMove);
      document.addEventListener("mouseup", handleMouseUp);
      document.body.style.userSelect = "none";
      document.body.style.cursor = "col-resize";
    }

    return () => {
      document.removeEventListener("mousemove", handleMouseMove);
      document.removeEventListener("mouseup", handleMouseUp);
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    };
  }, [isResizingWidth, setWidth, setIsResizingGlobal]);

  return (
    <div
      ref={panelRef}
      className="relative bg-card border-l border-border flex flex-col"
      style={{
        height: "100%", // Always take full height of the flex container
        width: `${sidebarWidth}px`,
        minHeight: "150px", // Ensure minimum usable height
      }}
    >
      {/* Resize Handle - on the LEFT side (for width) */}
      <div
        className={cn(
          "absolute left-0 top-0 bottom-0 w-1 cursor-col-resize hover:bg-primary/20 transition-colors z-20",
          isResizingWidth && "bg-primary/30"
        )}
        onMouseDown={(e) => {
          e.preventDefault();
          setIsResizingWidth(true);
          setIsResizingGlobal(true);
        }}
      >
        <div className="absolute inset-y-0 -left-1 -right-1" />
      </div>

      {/* Header - styled to match ChatTabs */}
      <div className="flex items-center justify-between px-3 py-0 bg-accent border-b border-border h-10 flex-shrink-0">
        <div className="flex items-center gap-2">
          <span className="text-xs font-mono">Terminal</span>
        </div>

        <div className="flex items-center gap-1">
          {/* Kill Active Terminal Button - only show if active session is in current worktree */}
          {activeSessionId && sessions.some((s) => s.id === activeSessionId) && (
            <Tooltip content="Kill active terminal" placement="bottom" delay={300}>
              <button
                aria-label="Kill active terminal"
                onClick={(e) => {
                  if (activeSessionId) {
                    handleKillTerminal(activeSessionId, e);
                  }
                }}
                className="p-1 hover:bg-destructive/20 hover:text-destructive-ink rounded transition-colors"
              >
                <Trash2 className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

          {/* New Terminal Button */}
          <Tooltip content="New terminal (Cmd+Shift+J)" placement="bottom" delay={300}>
            <button
              aria-label="New terminal"
              onClick={handleNewTerminal}
              disabled={workingDir.kind !== "ready"}
              className="p-1 hover:bg-accent/20 rounded transition-colors disabled:opacity-50 disabled:pointer-events-none"
            >
              <Plus className="w-4 h-4" />
            </button>
          </Tooltip>

          {/* Collapse Panel Button */}
          <Tooltip content="Hide terminal panel (Cmd+J)" placement="bottom" delay={300}>
            <button
              onClick={hideTerminal}
              className="p-1 hover:bg-accent/20 rounded transition-all"
              aria-label="Hide terminal panel"
            >
              <ChevronDown className="w-4 h-4" />
            </button>
          </Tooltip>
        </div>
      </div>

      {/* Content area with terminal list on right */}
      <div className="flex-1 flex overflow-hidden bg-background min-h-0">
        {/* Terminal display - render ALL project sessions to keep them alive */}
        <div className="flex-1 relative overflow-hidden">
          {/* Empty state for current worktree */}
          {sessions.length === 0 && (
            <div className="flex items-center justify-center h-full text-muted-foreground">
              <div className="text-center">
                <TerminalIcon size={48} className="mx-auto mb-4 opacity-50" />
                {workingDir.kind === "pending" ? (
                  <p className="text-sm">Waiting for the workspace to finish setting up…</p>
                ) : workingDir.kind === "failed" ? (
                  <p className="text-sm">This workspace failed to set up, so there is no directory to open a terminal in.</p>
                ) : (
                  <>
                    <p className="text-sm">No terminal sessions</p>
                    <p className="text-xs mt-2">
                      Click <Plus size={12} className="inline" /> to create a new
                      terminal
                    </p>
                  </>
                )}
              </div>
            </div>
          )}
          {/* Render ALL project terminals - show only the active one from current worktree */}
          {projectSessions.map((session) => (
            <Terminal
              key={session.id}
              sessionId={session.id}
              workingDir={session.workingDir}
              worktreeId={session.worktreeId}
              projectId={session.projectId}
              className={cn(
                "absolute inset-0",
                // Only show if this is the active session AND belongs to current worktree
                activeSessionId === session.id && session.worktreeId === currentWorktreeId
                  ? "block"
                  : "hidden"
              )}
            />
          ))}
        </div>

        {/* Terminal list sidebar */}
        <div className="w-6 border-l border-border flex flex-col flex-shrink-0">
          <div className="flex-1 overflow-y-auto py-1 flex flex-col items-center">
            {sessions.map((session) => {
              const isActive = activeSessionId === session.id;
              return (
                <div
                  key={session.id}
                  onClick={() => handleSetActiveSession(session.id)}
                  className={cn(
                    "group relative flex items-center justify-center w-4 h-4 mb-0.5 cursor-pointer transition-all rounded",
                    !isActive &&
                      "text-muted-foreground hover:bg-accent hover:text-foreground"
                  )}
                  style={
                    isActive
                      ? {
                          backgroundColor: `hsl(var(--tab-active) / 0.15)`,
                          color: `hsl(var(--tab-active))`,
                          boxShadow: "0 0 0 1px hsl(var(--tab-active) / 0.3)",
                        }
                      : undefined
                  }
                >
                  <TerminalIcon size={10} className="flex-shrink-0" />

                  {/* Tooltip on hover */}
                  <div className="absolute right-full mr-2 top-0 hidden group-hover:block z-50 pointer-events-none">
                    <div className="bg-popover text-popover-foreground border border-border rounded px-2 py-1.5 shadow-lg whitespace-nowrap text-xs">
                      <div className="font-semibold">{session.title}</div>
                      {session.pid && (
                        <div className="text-muted-foreground">
                          PID: {session.pid}
                        </div>
                      )}
                      <div className="text-muted-foreground">
                        Created:{" "}
                        {new Date(session.createdAt).toLocaleTimeString()}
                      </div>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}