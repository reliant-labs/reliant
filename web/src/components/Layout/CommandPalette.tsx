/**
 * CommandPalette — commands and settings search (Cmd+Shift+P).
 *
 * Rendered by the keyboard host (components/Shortcuts/ShortcutHost), so it
 * opens on every authenticated desktop route, not only in the workspace. That
 * makes its list a claim about the route it opens on: an entry that would do
 * nothing here — a workspace panel with no project, the page you are already
 * on — is not offered, and a workspace action offered from another page says
 * that it opens the workspace. Workspace actions run through the host, which
 * goes there first when it has to.
 */
import { useState, useRef, useEffect, useCallback } from "react";
import { createPortal } from "react-dom";
import { 
  Search, Settings, Keyboard, Info, Palette, Shield, 
  FolderOpen, GitBranch, Workflow, Bot, Terminal,
  Code, CalendarClock, Activity, Inbox
} from "lucide-react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { cn } from "../../lib/utils";
import { useViewerStore } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { focusChatInput } from "../../hooks/useFocusManager";
import { useShortcutHost } from "../../hooks/useShortcutHost";
import type { SettingsSection } from "../../routeSchemas";

interface Command {
  id: string;
  title: string;
  description: string;
  icon: React.ReactNode;
  category: "navigation" | "settings" | "actions";
  action: () => void;
  keywords?: string[];
  /** False where running it would do nothing. Defaults to true. */
  available?: boolean;
}

interface CommandPaletteProps {
  isOpen: boolean;
  onClose: () => void;
  /** Whether the workspace is the page behind the palette. */
  inWorkspace: boolean;
}

/** Element focused when the palette opened, if focus can go back to it. */
function restorableFocus(element: Element | null): HTMLElement | null {
  if (!(element instanceof HTMLElement) || element === document.body) return null;
  return element;
}

export function CommandPalette({ isOpen, onClose, inWorkspace }: CommandPaletteProps) {
    const [query, setQuery] = useState("");
    const [highlightedIndex, setHighlightedIndex] = useState(0);

    const inputRef = useRef<HTMLInputElement>(null);
    const modalContentRef = useRef<HTMLDivElement>(null);
    // Read during the opening render, before the search box takes focus.
    const [returnFocus] = useState(() => restorableFocus(document.activeElement));

    const currentProject = useProjectStore((state) => state.currentProject);
    const pathname = useRouterState({ select: (s) => s.location.pathname });
    const navigate = useNavigate();
    const host = useShortcutHost();
    
    // Focus the search box when opened.
    useEffect(() => {
      if (isOpen) {
        setQuery("");
        setHighlightedIndex(0);
        setTimeout(() => inputRef.current?.focus(), 50);
      }
    }, [isOpen]);

    const close = useCallback(() => {
      onClose();
      setQuery("");
    }, [onClose]);

    // Hand focus back to where it was when the palette goes away — however it
    // was closed, including the workspace's Escape, which closes it from
    // outside. In the workspace, the composer when it was nowhere in
    // particular. A command that took focus itself keeps it.
    const inWorkspaceRef = useRef(inWorkspace);
    inWorkspaceRef.current = inWorkspace;
    useEffect(() => () => {
      const active = document.activeElement;
      if (active && active !== document.body) return;
      if (returnFocus?.isConnected) returnFocus.focus();
      else if (inWorkspaceRef.current) focusChatInput();
    }, [returnFocus]);

    // Run a command: close first, so a command that moves focus (open the
    // terminal) keeps it.
    const runCommand = useCallback((command: Command) => {
      close();
      command.action();
    }, [close]);

    const goTo = (section: SettingsSection) => () =>
      navigate({ to: '/settings/$section', params: { section } });
    const isAt = (path: string) => pathname === path || pathname.startsWith(`${path}/`);

    /** Open a workspace viewer for the current project, going there first if needed. */
    const openViewer = (open: (projectId: string) => void) => () =>
      host?.runInWorkspace(() => {
        const projectId = useProjectStore.getState().currentProject?.id;
        if (projectId) open(projectId);
      });

    // Define all commands
    const commands: Command[] = [
      // Navigation
      {
        id: "projects",
        title: "Projects",
        description: inWorkspace ? "Open projects panel" : "Open the projects panel in the workspace",
        icon: <FolderOpen className="w-4 h-4" />,
        category: "navigation",
        keywords: ["project", "folder", "open"],
        available: Boolean(currentProject),
        action: openViewer((id) => useViewerStore.getState().openProjectsViewer(id)),
      },
      {
        id: "worktrees",
        title: "Workspaces",
        description: inWorkspace ? "Open workspaces panel" : "Open the workspaces panel in the workspace",
        icon: <GitBranch className="w-4 h-4" />,
        category: "navigation",
        keywords: ["worktree", "branch", "workspace"],
        available: Boolean(currentProject),
        action: openViewer((id) => useViewerStore.getState().openWorktreesViewer(id)),
      },
      {
        id: "workflows",
        title: "Workflow Builder",
        description: "Open workflow builder",
        icon: <Workflow className="w-4 h-4" />,
        category: "navigation",
        keywords: ["workflow", "automation", "flow"],
        available: pathname !== "/workflows" && !isAt("/workflows/library"),
        action: () => navigate({ to: '/workflows' }),
      },
      {
        id: "automations",
        title: "Automations",
        description: "Workflows that run on a schedule",
        icon: <CalendarClock className="w-4 h-4" />,
        category: "navigation",
        keywords: ["schedule", "trigger", "cron", "automation"],
        available: !isAt("/workflows/automations"),
        action: () => navigate({ to: '/workflows/automations' }),
      },
      {
        id: "inbox",
        title: "Inbox",
        description: "Approvals, questions and automation problems waiting on you",
        icon: <Inbox className="w-4 h-4" />,
        category: "navigation",
        keywords: ["approval", "question", "waiting", "needs you", "notifications"],
        available: !isAt("/inbox"),
        action: () => navigate({ to: '/inbox' }),
      },
      {
        id: "runs",
        title: "Runs",
        description: "Every run, including automation and agent runs",
        icon: <Activity className="w-4 h-4" />,
        category: "navigation",
        keywords: ["run", "history", "execution", "agent", "started"],
        available: !isAt("/workflows/runs"),
        action: () => navigate({ to: '/workflows/runs' }),
      },
      // Settings sections
      {
        id: "settings",
        title: "Settings",
        description: "Open settings",
        icon: <Settings className="w-4 h-4" />,
        category: "settings",
        keywords: ["preferences", "config", "configuration"],
        available: !isAt("/settings"),
        action: () => navigate({ to: '/settings' }),
      },
      {
        id: "shortcuts",
        title: "Keyboard Shortcuts",
        description: "View and customize shortcuts",
        icon: <Keyboard className="w-4 h-4" />,
        category: "settings",
        keywords: ["keys", "hotkeys", "bindings"],
        available: pathname !== "/settings/shortcuts",
        action: goTo('shortcuts'),
      },
      {
        id: "appearance",
        title: "Appearance",
        description: "Theme and display settings",
        icon: <Palette className="w-4 h-4" />,
        category: "settings",
        keywords: ["theme", "color", "dark", "light"],
        available: pathname !== "/settings/appearance",
        action: goTo('appearance'),
      },
      {
        id: "providers",
        title: "AI Providers",
        description: "Configure AI model providers",
        icon: <Bot className="w-4 h-4" />,
        category: "settings",
        keywords: ["api", "key", "openai", "anthropic", "claude"],
        available: pathname !== "/settings/general",
        action: goTo('general'),
      },
      {
        id: "privacy",
        title: "Privacy Settings",
        description: "Privacy and data settings",
        icon: <Shield className="w-4 h-4" />,
        category: "settings",
        keywords: ["data", "telemetry", "tracking"],
        available: pathname !== "/settings/privacy",
        action: goTo('privacy'),
      },
      {
        id: "developer",
        title: "Developer Settings",
        description: "Developer and debug options",
        icon: <Code className="w-4 h-4" />,
        category: "settings",
        keywords: ["debug", "dev", "advanced"],
        available: pathname !== "/settings/developer",
        action: goTo('developer'),
      },
      {
        id: "about",
        title: "About",
        description: "About Reliant",
        icon: <Info className="w-4 h-4" />,
        category: "settings",
        keywords: ["version", "info"],
        available: pathname !== "/settings/about",
        action: goTo('about'),
      },
      // Actions — the same handlers as their keyboard shortcuts. Off the
      // workspace the toggle opens the terminal there rather than toggling a
      // panel nobody can see (lib/keyboard/scope).
      {
        id: "toggle-terminal",
        title: inWorkspace ? "Toggle Terminal" : "Open Terminal",
        description: inWorkspace ? "Show or hide terminal" : "Show the terminal in the workspace",
        icon: <Terminal className="w-4 h-4" />,
        category: "actions",
        keywords: ["console", "shell", "bash"],
        available: Boolean(currentProject),
        action: () => host?.run("onToggleTerminal"),
      },
      {
        id: "new-terminal",
        title: "New Terminal",
        description: inWorkspace ? "Create a new terminal tab" : "Open a new terminal in the workspace",
        icon: <Terminal className="w-4 h-4" />,
        category: "actions",
        keywords: ["console", "shell", "bash"],
        available: Boolean(currentProject),
        action: () => host?.run("onNewTerminal"),
      },
    ];
    const allCommands = commands.filter((command) => command.available !== false);

    // Filter commands based on query
    const filteredCommands = query.trim()
      ? allCommands.filter((cmd) => {
          const searchStr = `${cmd.title} ${cmd.description} ${cmd.keywords?.join(" ") || ""}`.toLowerCase();
          return searchStr.includes(query.toLowerCase());
        })
      : allCommands;

    // Group commands by category
    const groupedCommands = filteredCommands.reduce((acc, cmd) => {
      if (!acc[cmd.category]) acc[cmd.category] = [];
      acc[cmd.category].push(cmd);
      return acc;
    }, {} as Record<string, Command[]>);

    const categoryTitles: Record<string, string> = {
      navigation: "Navigation",
      settings: "Settings",
      actions: "Actions",
    };

    // Close on click outside - works when clicking anywhere outside the modal
    useEffect(() => {
      if (!isOpen) return;

      const handleClickOutside = (e: MouseEvent) => {
        const target = e.target as Node;
        // Close if click is outside the modal content (but allow clicking the backdrop)
        if (modalContentRef.current && !modalContentRef.current.contains(target)) {
          close();
        }
      };

      // Use a small delay to avoid closing immediately when opening
      const timeoutId = setTimeout(() => {
        document.addEventListener("mousedown", handleClickOutside);
      }, 100);

      return () => {
        clearTimeout(timeoutId);
        document.removeEventListener("mousedown", handleClickOutside);
      };
    }, [isOpen, close]);

    // Reset highlight when results change
    useEffect(() => {
      setHighlightedIndex(0);
    }, [query]);

    // Handle keyboard navigation
    const handleKeyDown = (e: React.KeyboardEvent) => {
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          setHighlightedIndex((prev) => 
            prev < filteredCommands.length - 1 ? prev + 1 : 0
          );
          break;
        case "ArrowUp":
          e.preventDefault();
          setHighlightedIndex((prev) => 
            prev > 0 ? prev - 1 : filteredCommands.length - 1
          );
          break;
        case "Enter":
          e.preventDefault();
          if (filteredCommands[highlightedIndex]) {
            runCommand(filteredCommands[highlightedIndex]);
          }
          break;
        case "Escape":
          e.preventDefault();
          close();
          break;
      }
    };

    // Global Escape key handler - works even when modal loses focus
    useEffect(() => {
      if (!isOpen) return;

      const handleGlobalKeyDown = (e: KeyboardEvent) => {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          close();
        }
      };

      // Use capture phase to catch Escape before other handlers
      document.addEventListener("keydown", handleGlobalKeyDown, true);
      return () => {
        document.removeEventListener("keydown", handleGlobalKeyDown, true);
      };
    }, [isOpen, close]);

    // Auto-scroll highlighted item into view
    useEffect(() => {
      if (highlightedIndex >= 0 && modalContentRef.current) {
        const element = modalContentRef.current.querySelector(`[data-index="${highlightedIndex}"]`);
        element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
      }
    }, [highlightedIndex]);

    if (!isOpen) return null;

    return createPortal(
      <div 
        className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]"
        data-modal-open="true"
      >
        {/* Backdrop */}
        <div className="absolute inset-0 bg-black/50" />
        
        {/* Modal */}
        <div 
          ref={modalContentRef}
          className="relative w-full max-w-2xl bg-background border border-border rounded-lg shadow-2xl overflow-hidden"
        >
          {/* Search input */}
          <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
            <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
            <input
              ref={inputRef}
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder="Type a command..."
              className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
              autoFocus
            />
          </div>

          {/* Results */}
          <div className="max-h-[60vh] overflow-y-auto">
            {filteredCommands.length === 0 ? (
              <div className="px-4 py-8 text-center">
                <p className="text-sm text-muted-foreground font-mono">No commands found</p>
              </div>
            ) : (
              <div className="py-1">
                {Object.entries(groupedCommands).map(([category, commands]) => (
                  <div key={category}>
                    <div className="px-4 py-2 text-xs font-medium text-muted-foreground border-b border-border/50 bg-muted/30">
                      {categoryTitles[category] || category}
                    </div>
                    {commands.map((cmd) => {
                      const globalIndex = filteredCommands.indexOf(cmd);
                      return (
                        <button
                          key={cmd.id}
                          data-index={globalIndex}
                          onClick={() => runCommand(cmd)}
                          onMouseEnter={() => setHighlightedIndex(globalIndex)}
                          className={cn(
                            "w-full flex items-center gap-3 px-4 py-2 text-left transition-colors",
                            highlightedIndex === globalIndex 
                              ? "bg-accent border-2 border-primary rounded-md text-foreground" 
                              : "hover:bg-muted/50 border-2 border-transparent rounded-md"
                          )}
                        >
                          <div className={cn(
                            "flex-shrink-0",
                            highlightedIndex === globalIndex ? "text-primary" : "text-muted-foreground"
                          )}>
                            {cmd.icon}
                          </div>
                          <div className="flex-1 min-w-0">
                            <div className={cn(
                              "text-sm font-medium truncate",
                              highlightedIndex === globalIndex ? "text-foreground font-semibold" : ""
                            )}>{cmd.title}</div>
                            <div className={cn(
                              "text-xs truncate",
                              highlightedIndex === globalIndex ? "text-foreground/70" : "text-muted-foreground"
                            )}>{cmd.description}</div>
                          </div>
                        </button>
                      );
                    })}
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Footer */}
          <div className="px-4 py-2 border-t border-border bg-muted/30 flex items-center gap-3 text-xs text-muted-foreground">
            <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">↑↓</kbd> Navigate</span>
            <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Enter</kbd> Run</span>
            <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Esc</kbd> Close</span>
          </div>
        </div>
      </div>,
      document.body
    );
}