import { useState, useEffect } from "react";
import { X } from "lucide-react";
import { useShallow } from "zustand/react/shallow";
import { cn } from "../../lib/utils";
import { RightSidebar } from "../FileBrowser/RightSidebar";
import { RecentChanges } from "../Chat/RecentChanges";
import { useProjectStore } from "../../store/projectStore";
import { useChatStore } from "../../store/chatStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { useWorktreeStore } from "../../store/worktreeStore";

export type RightContentView = "files" | "changes" | null;

interface RightContentPanelProps {
  activeView: RightContentView;
  onClose: () => void;
}

export function RightContentPanel({
  activeView,
  onClose,
}: RightContentPanelProps) {
  const [width, setWidth] = useState(400);
  const [isResizing, setIsResizing] = useState(false);
  const setIsResizingGlobal = useSidebarStore((state) => state.setIsResizing);
  const currentProject = useProjectStore((state) => state.currentProject);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  
  // Get active chat's worktree, with fallback to pendingNewChatWorktreeId, then currentWorktree
  // Use useShallow to prevent infinite re-renders from object selector
  const { activeChatId, pendingNewChatWorktreeId, chats } = useChatStore(useShallow((state) => ({
    activeChatId: state.activeChatId,
    pendingNewChatWorktreeId: state.pendingNewChatWorktreeId,
    chats: state.chats,
  })));
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const mainWorktree = worktrees.find(w => w.is_main);
  const activeChat = activeChatId ? chats.get(activeChatId) ?? null : null;
  
  // Use same derivation logic as RightSidebar for consistency
  const activeWorktreeId = activeChat?.worktreeId || pendingNewChatWorktreeId || currentWorktree?.id || mainWorktree?.id;

  useEffect(() => {
    if (!isResizing) return;

    const handleMouseMove = (e: MouseEvent) => {
      // Calculate width from right edge
      const newWidth = window.innerWidth - e.clientX;
      // Constrain width between 300px and 800px
      setWidth(Math.max(300, Math.min(800, newWidth)));
    };

    const handleMouseUp = () => {
      setIsResizing(false);
      setIsResizingGlobal(false);
    };

    document.addEventListener("mousemove", handleMouseMove);
    document.addEventListener("mouseup", handleMouseUp);

    return () => {
      document.removeEventListener("mousemove", handleMouseMove);
      document.removeEventListener("mouseup", handleMouseUp);
    };
  }, [isResizing, setIsResizingGlobal]);

  if (!activeView) return null;

  const renderContent = () => {
    switch (activeView) {
      case "files":
        return (
          <div className="flex flex-col h-full">
            <div className="p-3 border-b border-border flex items-center justify-between">
              <h2 className="text-sm font-mono font-semibold uppercase text-muted-foreground">
                RELIANT
              </h2>
              <button
                onClick={onClose}
                className="p-1 hover:bg-accent rounded transition-colors"
                aria-label="Close"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="flex-1 overflow-hidden">
              <RightSidebar />
            </div>
          </div>
        );
      case "changes":
        return (
          <div className="flex flex-col h-full">
            <div className="p-3 border-b border-border flex items-center justify-between">
              <h2 className="text-sm font-mono font-semibold uppercase text-muted-foreground">
                RELIANT
              </h2>
              <button
                onClick={onClose}
                className="p-1 hover:bg-accent rounded transition-colors"
                aria-label="Close"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="flex-1 overflow-hidden">
              <RecentChanges
                onClose={onClose}
                worktreeId={activeWorktreeId}
                projectId={currentProject?.id || ""}
              />
            </div>
          </div>
        );
      default:
        return null;
    }
  };

  return (
    <>
      {/* Resize handle */}
      <div
        className={cn(
          "w-1 cursor-col-resize hover:bg-primary/50 transition-colors flex-shrink-0",
          isResizing && "bg-primary"
        )}
        onMouseDown={() => {
          setIsResizing(true);
          setIsResizingGlobal(true);
        }}
      />

      {/* Panel content */}
      <div
        className="flex flex-col h-full bg-background border-l border-border flex-shrink-0"
        style={{ width: `${width}px` }}
      >
        {renderContent()}
      </div>
    </>
  );
}
