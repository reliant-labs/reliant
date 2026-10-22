import { useState, useEffect } from "react";
import { X } from "lucide-react";
import { cn } from "../../lib/utils";
import { RightSidebar } from "../FileBrowser/RightSidebar";
import { RecentChanges } from "../Chat/RecentChanges";
import { useProjectStore } from "../../store/projectStore";
import { useSidebarStore } from "../../store/sidebarStore";
import { useActiveWorktreeId } from "../../store/worktreeStore";

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
  // Use the global active worktree (from worktreeStore) as the single source of truth
  const activeWorktreeId = useActiveWorktreeId();

  const renderPanelHeader = () => (
    <div className="flex h-11 items-center justify-between border-b border-border bg-card px-3">
      <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Reliant
      </h2>
      <button
        onClick={onClose}
        className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        aria-label="Close"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  );

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
            {renderPanelHeader()}
            <div className="flex-1 overflow-hidden">
              <RightSidebar />
            </div>
          </div>
        );
      case "changes":
        return (
          <div className="flex flex-col h-full">
            {renderPanelHeader()}
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