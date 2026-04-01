import { useState, useRef, useEffect } from "react";
import {
  ChevronDown,
  Check,
  Plus,
  FolderGit2,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { AgentSelector } from "./AgentSelector";
import { Tooltip } from "../ui/Tooltip";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";

interface UnifiedPillSelectorProps {
  // Agent props (single selection - orchestrators have sub_agents)
  selectedAgent?: string | null;
  onAgentChange?: (agent: string | null) => void;

  // Worktree props
  selectedWorktreeId?: string;
  onWorktreeSelect?: (worktreeId: string | null) => void;

  // General props
  isStreaming?: boolean;
  isNewChat?: boolean;
  chatId?: string;
  className?: string;
  compact?: boolean;
}

export function UnifiedPillSelector({
  selectedAgent,
  onAgentChange,
  selectedWorktreeId,
  onWorktreeSelect,
  isStreaming = false,
  isNewChat = false,
  chatId: _chatId,
  className = "",
  compact = false,
}: UnifiedPillSelectorProps) {
  const [showWorktreeDropdown, setShowWorktreeDropdown] = useState(false);
  const [showCreateModal, setShowCreateModal] = useState(false);

  const worktreeDropdownRef = useRef<HTMLDivElement>(null);
  const worktreeContainerRef = useRef<HTMLDivElement>(null);

  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  // Filter out archived worktrees (only show active ones)
  const worktrees = allWorktrees.filter(w => !w.deleted_at);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const currentProject = useProjectStore((state) => state.currentProject);

  // Load worktrees
  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  }, [currentProject, loadWorktrees]);

  const selectedWorktree = selectedWorktreeId
    ? worktrees.find((w) => w.id === selectedWorktreeId)
    : isNewChat
    ? currentWorktree
    : null;

  const getWorktreeStatusLabel = (status: WorktreeStatus): string => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return "active";
      case WorktreeStatus.COMPLETED:
        return "completed";
      case WorktreeStatus.MERGING:
        return "merging";
      case WorktreeStatus.ABANDONED:
        return "abandoned";
      default:
        return "unknown";
    }
  };

  const formatWorktreeName = (worktree: typeof selectedWorktree) => {
    if (!worktree) return currentProject?.default_branch || "main";
    return worktree.branch || worktree.name;
  };

  const handleWorktreeSelect = (worktreeId: string | null) => {
    onWorktreeSelect?.(worktreeId);
    setShowWorktreeDropdown(false);
  };

  const handleWorktreeCreated = (worktreeId: string) => {
    setShowCreateModal(false);
    // Select the newly created worktree
    handleWorktreeSelect(worktreeId);
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  };

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (worktreeContainerRef.current && !worktreeContainerRef.current.contains(event.target as Node)) {
        setShowWorktreeDropdown(false);
      }
    };

    if (showWorktreeDropdown) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [showWorktreeDropdown]);

  // Auto-scroll for worktree dropdown
  useEffect(() => {
    if (showWorktreeDropdown && worktreeDropdownRef.current) {
      const selectedButton =
        worktreeDropdownRef.current.querySelector(".dropdown-selected");
      if (selectedButton) {
        selectedButton.scrollIntoView({ block: "center", behavior: "auto" });
      }
    }
  }, [showWorktreeDropdown]);

  if (!currentProject) {
    return (
      <div className={`text-xs text-muted-foreground font-mono ${className}`}>
        No Project
      </div>
    );
  }

  // Workspace can only be set on new chats
  const canSelectWorktree = !isStreaming && isNewChat && onWorktreeSelect;

  return (
    <div className={`relative ${className}`}>
      <div className="flex items-center gap-2">
        {/* Agent Selector */}
        <AgentSelector
          value={selectedAgent}
          onChange={onAgentChange}
          isStreaming={isStreaming}
          compact={compact}
        />

        {/* Worktree Selector */}
        <div className="relative" ref={worktreeContainerRef} data-dropdown-open={showWorktreeDropdown}>
          <Tooltip 
            content={
              canSelectWorktree 
                ? "Select a git workspace for this chat" 
                : "Workspace can only be set on new chats"
            } 
            placement="top"
          >
            <button
              onClick={() => {
                if (canSelectWorktree) {
                  setShowWorktreeDropdown(!showWorktreeDropdown);
                }
              }}
              disabled={!canSelectWorktree}
              className={cn(
                "flex items-center gap-1.5 rounded transition-colors text-[10px] font-medium h-6",
                canSelectWorktree
                  ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
                  : "cursor-default opacity-60",
                isStreaming
                  ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
                  : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
                compact ? "px-1.5 gap-0.5" : "px-2 gap-1"
              )}
            >
            <FolderGit2 className="w-3 h-3 flex-shrink-0" />
            {!compact && (
              <span className="truncate max-w-24">
                {formatWorktreeName(selectedWorktree)}
              </span>
            )}
            {canSelectWorktree && (
              <ChevronDown
                className={`opacity-50 flex-shrink-0 ${
                  compact ? "w-2.5 h-2.5" : "w-3 h-3"
                }`}
              />
            )}
            </button>
          </Tooltip>

          {/* Worktree Dropdown */}
          {showWorktreeDropdown && canSelectWorktree && (
            <div
              className="absolute left-0 rounded-md elevation-4 z-[1000] bottom-full mb-1 border border-border/50 min-w-80 bg-[var(--chat-dropdown-bg)]"
            >
              <div
                ref={worktreeDropdownRef}
                className="py-1 max-h-96 overflow-y-auto rounded-md"
              >
                  {/* No worktree option */}
                  <button
                    onClick={() => handleWorktreeSelect(null)}
                    className={cn(
                      "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-[var(--chat-button-hover)]",
                      !selectedWorktreeId && "dropdown-selected bg-[var(--chat-button-hover)]"
                    )}
                    style={{
                      borderColor: "hsl(var(--border) / 0.2)",
                      color: "var(--chat-button-text)",
                    }}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="flex-1">
                        <div className="font-semibold mb-1">{currentProject?.default_branch || "main"}</div>
                        <div className="leading-snug text-[11px] opacity-80">
                          Main branch context
                        </div>
                      </div>
                      {!selectedWorktreeId && (
                        <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                      )}
                    </div>
                  </button>

                  {/* Worktree options */}
                  {worktrees.length === 0 ? (
                    <div className="px-3 py-2 text-xs opacity-80 border-b">
                      No workspaces available
                    </div>
                  ) : (
                    worktrees.map((worktree) => (
                      <button
                        key={worktree.id}
                        onClick={() => handleWorktreeSelect(worktree.id)}
                        className={cn(
                          "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-[var(--chat-button-hover)]",
                          selectedWorktreeId === worktree.id && "dropdown-selected bg-[var(--chat-button-hover)]"
                        )}
                        style={{
                          borderColor: "hsl(var(--border) / 0.2)",
                          color: "var(--chat-button-text)",
                        }}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="flex-1 min-w-0">
                            <div className="font-semibold mb-1 truncate">
                              {worktree.branch}
                            </div>
                            <div className="leading-snug text-[11px] opacity-80 truncate">
                              {worktree.name}
                            </div>
                          </div>
                          <div className="flex items-center gap-2 flex-shrink-0">
                            <span
                              className={`px-1.5 py-0.5 rounded text-[10px] capitalize ${
                                worktree.status === WorktreeStatus.ACTIVE
                                  ? "bg-success/10 text-success"
                                  : worktree.status === WorktreeStatus.COMPLETED
                                  ? "bg-primary/10 text-primary"
                                  : worktree.status === WorktreeStatus.MERGING
                                  ? "bg-primary/10 text-primary"
                                  : "bg-warning/10 text-warning"
                              }`}
                            >
                              {getWorktreeStatusLabel(worktree.status)}
                            </span>
                            {selectedWorktreeId === worktree.id && (
                              <Check className="w-3 h-3 text-primary" />
                            )}
                          </div>
                        </div>
                      </button>
                    ))
                  )}

                  {/* Create new workspace */}
                  <button
                    onClick={() => {
                      setShowWorktreeDropdown(false);
                      setShowCreateModal(true);
                    }}
                    className="w-full px-3 py-2 text-left text-xs transition-colors text-primary hover:bg-[var(--chat-button-hover)]"
                    style={{
                      color: "var(--primary)",
                    }}
                  >
                    <div className="flex items-center gap-2">
                      <Plus className="w-3 h-3" />
                      <span>Create new workspace</span>
                    </div>
                  </button>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Create Worktree Modal */}
      {currentProject && isNewChat && (
        <CreateWorktreeModal
          isOpen={showCreateModal}
          onClose={() => setShowCreateModal(false)}
          onWorktreeCreated={handleWorktreeCreated}
          projectId={currentProject.id}
        />
      )}
    </div>
  );
}
