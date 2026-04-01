import { useState, useEffect, useRef, useCallback } from 'react';
import { Plus, ChevronDown, Trash2, Terminal as TerminalIcon } from 'lucide-react';
import { cn } from '../../lib/utils';
import { Terminal } from './Terminal';
import { useTerminalStore } from '../../store/terminalStore';
import { useProjectStore } from '../../store/projectStore';
import { useSidebarStore } from '../../store/sidebarStore';
import { Tooltip } from '../ui/Tooltip';

export function ResizableTerminalPanel() {
  const [height, setHeight] = useState(() => {
    const saved = localStorage.getItem('terminal-panel-height');
    return saved ? parseInt(saved, 10) : 300;
  });
  const [isResizing, setIsResizing] = useState(false);
  const setIsResizingGlobal = useSidebarStore((state) => state.setIsResizing);
  const panelRef = useRef<HTMLDivElement>(null);
  const MIN_MAIN_CONTENT_HEIGHT = 200;

  const sessions = useTerminalStore((state) => state.sessions);
  const activeSessionId = useTerminalStore((state) => state.activeSessionId);
  const createSession = useTerminalStore((state) => state.createSession);
  const closeSession = useTerminalStore((state) => state.closeSession);
  const setActiveSession = useTerminalStore((state) => state.setActiveSession);
  const closeTerminal = useTerminalStore((state) => state.closeTerminal);
  const currentProject = useProjectStore((state) => state.currentProject);

  const minHeight = 200;
  const maxHeight = 800;

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isResizing || !panelRef.current) return;

      const containerBottom = window.innerHeight;
      const newHeight = containerBottom - e.clientY;

      // Safety check: Ensure we don't consume the entire screen vertical space
      // Leave at least MIN_MAIN_CONTENT_HEIGHT for the main content
      const maxSafeHeight = window.innerHeight - MIN_MAIN_CONTENT_HEIGHT;
      const effectiveMaxHeight = Math.min(maxHeight, maxSafeHeight);

      if (newHeight >= minHeight && newHeight <= effectiveMaxHeight) {
        setHeight(newHeight);
      }
    };

    const handleMouseUp = () => {
      setIsResizing(false);
      setIsResizingGlobal(false);
      localStorage.setItem('terminal-panel-height', height.toString());
    };

    if (isResizing) {
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'row-resize';
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };
  }, [isResizing, height, setIsResizingGlobal]);

  // Create a session on mount if none exist
  const hasCreatedInitialSession = useRef(false);

  useEffect(() => {
    if (sessions.length === 0 && !hasCreatedInitialSession.current) {
      hasCreatedInitialSession.current = true;
      const workingDir = currentProject?.path;
      createSession(workingDir);
    }
  }, [sessions.length, currentProject?.path, createSession]);

  const handleNewTerminal = useCallback(() => {
    const workingDir = currentProject?.path;
    createSession(workingDir);
  }, [currentProject?.path, createSession]);

  const handleKillTerminal = useCallback((sessionId: string, e: React.MouseEvent) => {
    e.stopPropagation();
    closeSession(sessionId);

    // If no sessions left, close the panel
    if (sessions.length === 1) {
      closeTerminal();
    }
  }, [closeSession, closeTerminal, sessions.length]);

  return (
    <div
      ref={panelRef}
      className="relative bg-card flex flex-col border-t border-border"
      style={{ height: `${height}px` }}
    >
      {/* Global Overlay for iframe protection during resize */}
      {isResizing && (
        <div className="fixed inset-0 z-[9999] cursor-row-resize bg-transparent" />
      )}

      {/* Resize Handle - on the TOP */}
      <div
        className={cn(
          "absolute top-0 left-0 right-0 h-1 cursor-row-resize hover:bg-primary/50 transition-colors z-10 group",
          isResizing && "bg-primary/80 h-1.5"
        )}
        onMouseDown={(e) => {
          e.preventDefault();
          setIsResizing(true);
          setIsResizingGlobal(true);
        }}
      >
        <div className="absolute inset-x-0 -top-1 -bottom-1" />
      </div>

      {/* Header */}
      <div className="flex items-center justify-between px-3 bg-accent border-b border-border h-10">
        <div className="flex items-center gap-2">
          <span className="text-xs font-mono">Terminal</span>
        </div>

        <div className="flex items-center gap-1">
          {/* Kill Active Terminal Button */}
          {activeSessionId && (
            <Tooltip content="Kill Active Terminal" placement="bottom">
              <button
                onClick={(e) => {
                  if (activeSessionId) {
                    handleKillTerminal(activeSessionId, e);
                  }
                }}
                className="p-1 hover:bg-destructive/20 hover:text-destructive rounded text-muted-foreground transition-colors"
              >
                <Trash2 size={14} />
              </button>
            </Tooltip>
          )}

          {/* New Terminal Button */}
          <Tooltip content="New Terminal (Ctrl+Shift+`)" placement="bottom">
            <button
              onClick={handleNewTerminal}
              className="p-1 hover:bg-accent rounded text-muted-foreground hover:text-foreground transition-colors"
            >
              <Plus size={14} />
            </button>
          </Tooltip>

          {/* Hide Panel Button */}
          <Tooltip content="Hide Panel (Cmd+J)" placement="bottom">
            <button
              onClick={closeTerminal}
              className="p-1 hover:bg-accent rounded text-muted-foreground hover:text-foreground transition-colors"
            >
              <ChevronDown size={14} />
            </button>
          </Tooltip>
        </div>
      </div>

      {/* Content area with terminal list on right */}
      <div className="flex-1 flex overflow-hidden bg-background">
        {/* Terminal display */}
        <div className="flex-1 relative overflow-hidden">
          {sessions.map((session) => (
            <Terminal
              key={session.id}
              sessionId={session.id}
              workingDir={session.workingDir}
              className={cn(
                "absolute inset-0",
                activeSessionId === session.id ? "block" : "hidden"
              )}
            />
          ))}
        </div>

        {/* Terminal list sidebar */}
        <div className="w-12 border-l border-border bg-muted flex flex-col">
          <div className="flex-1 overflow-y-auto py-1 px-1">
            {sessions.map((session) => {
              const isActive = activeSessionId === session.id;
              return (
                <div
                  key={session.id}
                  onClick={() => setActiveSession(session.id)}
                  className={cn(
                    "group relative flex items-center justify-center p-2 mb-1 cursor-pointer transition-all rounded",
                    isActive
                      ? "shadow-sm"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground"
                  )}
                  style={isActive ? {
                    backgroundColor: `hsl(var(--tab-active) / 0.2)`,
                    color: `hsl(var(--tab-active))`
                  } : undefined}
                >
                  <TerminalIcon size={16} className="flex-shrink-0" />

                  {/* Tooltip on hover */}
                  <div className="absolute left-full ml-2 top-0 hidden group-hover:block z-50 pointer-events-none">
                    <div className="bg-popover text-popover-foreground border border-border rounded px-2 py-1.5 shadow-lg whitespace-nowrap text-xs">
                      <div className="font-semibold">{session.title}</div>
                      {session.pid && <div className="text-muted-foreground">PID: {session.pid}</div>}
                      <div className="text-muted-foreground">Created: {new Date(session.createdAt).toLocaleTimeString()}</div>
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
