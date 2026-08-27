import { useState, useEffect, useRef } from "react";
import { ChevronDown, Plus, Check, FolderGit2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { Tooltip } from "../ui/Tooltip";

interface WorktreeSelectorProps {
  selectedWorktreeId?: string;
  onWorktreeSelect?: (worktreeId: string | null) => void;
  isNewChat?: boolean;
  compact?: boolean;
  className?: string;
}

export function WorktreeSelector({
  selectedWorktreeId,
  onWorktreeSelect,
  isNewChat = false,
  compact = false,
  className = "",
}: WorktreeSelectorProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [showCreateModal, setShowCreateModal] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  // Filter out archived worktrees (only show active ones)
  const activeWorktrees = allWorktrees.filter(w => !w.deleted_at);
  // Find the main worktree
  const mainWorktree = activeWorktrees.find(w => w.is_main === true);
  // Filter out the main worktree from the list (it's shown separately at the top)
  const nonMainWorktrees = activeWorktrees.filter(w => !w.is_main);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const currentProject = useProjectStore((state) => state.currentProject);

  const selectedWorktree = selectedWorktreeId
    ? activeWorktrees.find((w) => w.id === selectedWorktreeId)
    : currentWorktree;

  const handleWorktreeSelect = async (worktreeId: string | null) => {
    // When null is passed ("main" option), use the main worktree's ID
    const effectiveWorktreeId = worktreeId || mainWorktree?.id || null;
    onWorktreeSelect?.(effectiveWorktreeId);

    // Also update the global current worktree in the store
    if (currentProject && effectiveWorktreeId) {
      const worktree = activeWorktrees.find(w => w.id === effectiveWorktreeId);
      if (worktree) {
        await switchWorktreeContext(currentProject.id, worktree);
      }
    } else if (currentProject) {
      // Switch to main context
      await switchWorktreeContext(currentProject.id, null);
    }

    setIsOpen(false);
  };

  const handleWorktreeCreated = async (worktreeId: string) => {
    setShowCreateModal(false);
    // Reload worktrees first so the new one is in the list
    if (currentProject) {
      await loadWorktrees(currentProject.id);
    }
    // Then select the newly created worktree
    handleWorktreeSelect(worktreeId);
  };

  // Close dropdown when clicking outside - STANDARD PATTERN
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  // Load worktrees when component mounts or project changes
  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  }, [currentProject, loadWorktrees]);

  if (!currentProject) {
    return null;
  }

  const canSelectWorktree = isNewChat && onWorktreeSelect;

  return (
    <div className={`relative ${className}`} ref={containerRef} data-dropdown-open={isOpen}>
      <Tooltip
        content={
          canSelectWorktree
            ? "Select a git workspace for this chat"
            : "Workspace can only be set on new chats"
        }
        placement="top"
      >
        <button
        onClick={() => canSelectWorktree && setIsOpen(!isOpen)}
        disabled={!canSelectWorktree}
        className={cn(
          "flex items-center gap-1.5 rounded transition-colors text-2xs font-medium h-6",
          canSelectWorktree
            ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
            : "cursor-default opacity-60",
          "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
          compact ? "px-1.5 gap-0.5" : "px-2 gap-1"
        )}
      >
        <FolderGit2 className={compact ? "w-2.5 h-2.5" : "w-3 h-3"} />
        {!compact && (
          <span className="truncate max-w-24">
            {selectedWorktree ? selectedWorktree.branch : (currentProject?.default_branch || 'main')}
          </span>
        )}
        {canSelectWorktree && (
          <ChevronDown className={cn("opacity-50", compact ? "w-2.5 h-2.5" : "w-3 h-3")} />
        )}
      </button>
      </Tooltip>

      {isOpen && canSelectWorktree && (
        <div
          className="absolute bottom-full left-0 mb-1 border border-border/50 rounded-md elevation-4 z-[1000] min-w-80 bg-[var(--chat-dropdown-bg)] overflow-hidden"
        >
          <div className="overflow-y-auto max-h-80">
            {/* Main workspace option */}
            <button
              onClick={() => handleWorktreeSelect(null)}
              className={cn(
                "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-[var(--chat-button-hover)]",
                selectedWorktreeId === mainWorktree?.id && "bg-[var(--chat-button-hover)]"
              )}
              style={{
                borderColor: "hsl(var(--border) / 0.2)",
                color: "var(--chat-button-text)",
              }}
            >
              <div className="flex items-start justify-between gap-2">
                <div className="flex-1">
                  <div className="font-semibold mb-1">{mainWorktree?.branch || currentProject?.default_branch || 'main'}</div>
                  <div className="leading-snug text-xs opacity-80">Main workspace</div>
                </div>
                {selectedWorktreeId === mainWorktree?.id && (
                  <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                )}
              </div>
            </button>

            {/* Other workspaces (excludes main) */}
            {nonMainWorktrees.length === 0 ? (
              <div className="px-3 py-2 text-xs opacity-80 border-b" style={{ borderColor: "hsl(var(--border) / 0.2)" }}>
                No additional workspaces
              </div>
            ) : (
              nonMainWorktrees.map((worktree) => (
                <button
                  key={worktree.id}
                  onClick={() => handleWorktreeSelect(worktree.id)}
                  className={cn(
                    "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-[var(--chat-button-hover)]",
                    selectedWorktreeId === worktree.id && "bg-[var(--chat-button-hover)]"
                  )}
                  style={{
                    borderColor: "hsl(var(--border) / 0.2)",
                    color: "var(--chat-button-text)",
                  }}
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex-1">
                      <div className="font-semibold mb-1">{worktree.branch || worktree.name}</div>

                    </div>
                    {selectedWorktreeId === worktree.id && (
                      <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                    )}
                  </div>
                </button>
              ))
            )}

            {/* Create new workspace option */}
            <button
              onClick={() => {
                setShowCreateModal(true);
                setIsOpen(false);
              }}
              className="w-full px-3 py-2 text-left text-xs transition-colors hover:bg-[var(--chat-button-hover)] border-t"
              style={{
                borderColor: "hsl(var(--border) / 0.2)",
                color: "var(--chat-button-text)",
              }}
            >
              <div className="flex items-center gap-2">
                <Plus className="w-3 h-3" />
                <span className="font-semibold">Create new workspace</span>
              </div>
            </button>
          </div>
        </div>
      )}



      {/* Create Workspace Modal */}
      {currentProject && (
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