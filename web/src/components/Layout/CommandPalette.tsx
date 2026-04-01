// CommandPalette - Commands and settings search (Cmd+Shift+P)
import { useState, useRef, useEffect, forwardRef, useImperativeHandle, useCallback } from "react";
import { createPortal } from "react-dom";
import { 
  Search, Settings, Keyboard, Info, Palette, Shield, 
  FolderOpen, GitBranch, Workflow, Bot, Terminal,
  Code, MessageSquare
} from "lucide-react";
import { cn } from "../../lib/utils";
import { useViewerStore } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { focusChatInput } from "../../hooks/useFocusManager";

interface Command {
  id: string;
  title: string;
  description: string;
  icon: React.ReactNode;
  category: "navigation" | "settings" | "actions";
  action: () => void;
  keywords?: string[];
}

export interface CommandPaletteRef {
  open: () => void;
  close: () => void;
  isOpen: () => boolean;
}

interface CommandPaletteProps {
  isOpen?: boolean;
  onClose?: () => void;
  onNavigateToSettings?: () => void;
  onNavigateToSettingsSection?: (section: string) => void;
  onNavigateToWorktrees?: () => void;
  onNavigateToProjects?: () => void;
  onOpenWorkflows?: () => void;
  onToggleTerminal?: () => void;
  onNewTerminal?: () => void;
}

export const CommandPalette = forwardRef<CommandPaletteRef, CommandPaletteProps>(
  ({ 
    isOpen: externalIsOpen,
    onClose,
    onNavigateToSettings: _onNavigateToSettings,
    onNavigateToSettingsSection: _onNavigateToSettingsSection,
    onNavigateToWorktrees: _onNavigateToWorktrees,
    onNavigateToProjects: _onNavigateToProjects,
    onOpenWorkflows: _onOpenWorkflows,
    onToggleTerminal,
    onNewTerminal,
  }, ref) => {
    const [internalIsOpen, setInternalIsOpen] = useState(false);
    
    // Use external control if provided, otherwise use internal state
    const isOpen = externalIsOpen ?? internalIsOpen;
    const setIsOpen = useCallback((value: boolean) => {
      setInternalIsOpen(value);
      if (!value && onClose) {
        onClose();
      }
    }, [onClose]);
    const [query, setQuery] = useState("");
    const [highlightedIndex, setHighlightedIndex] = useState(0);

    const inputRef = useRef<HTMLInputElement>(null);
    const modalContentRef = useRef<HTMLDivElement>(null);
    
    const currentProject = useProjectStore((state) => state.currentProject);
    const setSettingsMode = useViewerStore((state) => state.setSettingsMode);
    const setWorkflowMode = useViewerStore((state) => state.setWorkflowMode);
    
    // Sync with external isOpen state and focus input when opened
    useEffect(() => {
      if (externalIsOpen) {
        setQuery("");
        setHighlightedIndex(0);
        setTimeout(() => inputRef.current?.focus(), 50);
      }
    }, [externalIsOpen]);

    // Close helper
    const closeAndFocus = useCallback(() => {
      setIsOpen(false);
      setQuery("");
      focusChatInput();
    }, [setIsOpen]);

    // Define all commands
    const allCommands: Command[] = [
      // Navigation
      {
        id: "projects",
        title: "Projects",
        description: "Open projects panel",
        icon: <FolderOpen className="w-4 h-4" />,
        category: "navigation",
        keywords: ["project", "folder", "open"],
        action: () => {
          if (currentProject?.id) {
            useViewerStore.getState().openProjectsViewer(currentProject.id);
          }
          closeAndFocus();
        },
      },
      {
        id: "worktrees",
        title: "Workspaces",
        description: "Open workspaces panel",
        icon: <GitBranch className="w-4 h-4" />,
        category: "navigation",
        keywords: ["worktree", "branch", "workspace"],
        action: () => {
          if (currentProject?.id) {
            useViewerStore.getState().openWorktreesViewer(currentProject.id);
          }
          closeAndFocus();
        },
      },
      {
        id: "workflows",
        title: "Workflow Builder",
        description: "Open workflow builder",
        icon: <Workflow className="w-4 h-4" />,
        category: "navigation",
        keywords: ["workflow", "automation", "flow"],
        action: () => {
          setWorkflowMode(true);
          closeAndFocus();
        },
      },
      // Settings sections
      {
        id: "settings",
        title: "Settings",
        description: "Open settings",
        icon: <Settings className="w-4 h-4" />,
        category: "settings",
        keywords: ["preferences", "config", "configuration"],
        action: () => {
          setSettingsMode(true);
          closeAndFocus();
        },
      },
      {
        id: "shortcuts",
        title: "Keyboard Shortcuts",
        description: "View and customize shortcuts",
        icon: <Keyboard className="w-4 h-4" />,
        category: "settings",
        keywords: ["keys", "hotkeys", "bindings"],
        action: () => {
          setSettingsMode(true, "shortcuts");
          closeAndFocus();
        },
      },
      {
        id: "appearance",
        title: "Appearance",
        description: "Theme and display settings",
        icon: <Palette className="w-4 h-4" />,
        category: "settings",
        keywords: ["theme", "color", "dark", "light"],
        action: () => {
          setSettingsMode(true, "appearance");
          closeAndFocus();
        },
      },
      {
        id: "providers",
        title: "AI Providers",
        description: "Configure AI model providers",
        icon: <Bot className="w-4 h-4" />,
        category: "settings",
        keywords: ["api", "key", "openai", "anthropic", "claude"],
        action: () => {
          setSettingsMode(true, "general");
          closeAndFocus();
        },
      },
      {
        id: "privacy",
        title: "Privacy Settings",
        description: "Privacy and data settings",
        icon: <Shield className="w-4 h-4" />,
        category: "settings",
        keywords: ["data", "telemetry", "tracking"],
        action: () => {
          setSettingsMode(true, "privacy");
          closeAndFocus();
        },
      },
      {
        id: "developer",
        title: "Developer Settings",
        description: "Developer and debug options",
        icon: <Code className="w-4 h-4" />,
        category: "settings",
        keywords: ["debug", "dev", "advanced"],
        action: () => {
          setSettingsMode(true, "developer");
          closeAndFocus();
        },
      },
      {
        id: "about",
        title: "About",
        description: "About Reliant",
        icon: <Info className="w-4 h-4" />,
        category: "settings",
        keywords: ["version", "info"],
        action: () => {
          setSettingsMode(true, "about");
          closeAndFocus();
        },
      },
      {
        id: "feedback",
        title: "Send Feedback",
        description: "Report bugs or suggest features",
        icon: <MessageSquare className="w-4 h-4" />,
        category: "settings",
        keywords: ["bug", "report", "feature", "request", "help", "support"],
        action: () => {
          setSettingsMode(true, "feedback");
          closeAndFocus();
        },
      },
      // Actions
      {
        id: "toggle-terminal",
        title: "Toggle Terminal",
        description: "Show or hide terminal",
        icon: <Terminal className="w-4 h-4" />,
        category: "actions",
        keywords: ["console", "shell", "bash"],
        action: () => {
          onToggleTerminal?.();
          closeAndFocus();
        },
      },
      {
        id: "new-terminal",
        title: "New Terminal",
        description: "Create a new terminal tab",
        icon: <Terminal className="w-4 h-4" />,
        category: "actions",
        keywords: ["console", "shell", "bash"],
        action: () => {
          onNewTerminal?.();
          closeAndFocus();
        },
      },
    ];

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

    // Expose methods via ref
    useImperativeHandle(ref, () => ({
      open: () => {
        setIsOpen(true);
        setQuery("");
        setHighlightedIndex(0);
        setTimeout(() => inputRef.current?.focus(), 50);
      },
      close: () => {
        setIsOpen(false);
        setQuery("");
      },
      isOpen: () => isOpen,
    }), [isOpen, setIsOpen]);

    // Close on click outside - works when clicking anywhere outside the modal
    useEffect(() => {
      if (!isOpen) return;

      const handleClickOutside = (e: MouseEvent) => {
        const target = e.target as Node;
        // Close if click is outside the modal content (but allow clicking the backdrop)
        if (modalContentRef.current && !modalContentRef.current.contains(target)) {
          closeAndFocus();
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
    }, [isOpen, closeAndFocus]);

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
            filteredCommands[highlightedIndex].action();
          }
          break;
        case "Escape":
          e.preventDefault();
          closeAndFocus();
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
          closeAndFocus();
        }
      };

      // Use capture phase to catch Escape before other handlers
      document.addEventListener("keydown", handleGlobalKeyDown, true);
      return () => {
        document.removeEventListener("keydown", handleGlobalKeyDown, true);
      };
    }, [isOpen, closeAndFocus]);

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
                          onClick={() => cmd.action()}
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
);

CommandPalette.displayName = "CommandPalette";
