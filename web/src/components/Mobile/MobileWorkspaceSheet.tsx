/**
 * Chat-header drill-in for the four workspace panels (Files / Git / Plan /
 * Packages) that only exist as desktop sidebar tabs today. These are
 * workspace context for the open chat, so they live behind the chat header
 * rather than in the (soon-to-be-removed) tab bar or a global nav item.
 *
 * Full-screen rather than a bottom sheet — a file tree or a diff needs the
 * whole viewport on a phone, not a fixed-height drawer.
 */

import { useState } from "react";
import { createPortal } from "react-dom";
import { X, Files, GitBranch, ListTodo, Terminal } from "lucide-react";
import { cn } from "../../lib/utils";
import { useCapability } from "../../lib/surfaceContext";
import { MobileFilesPanel } from "./MobileFilesPanel";
import { GitStatus } from "../Git/GitStatus";
import { TasksPanel } from "../Chat/TasksPanel";
import { MobilePackagesPanel } from "./MobilePackagesPanel";

type WorkspaceTab = "files" | "git" | "plan" | "packages";

interface MobileWorkspaceSheetProps {
  isOpen: boolean;
  onClose: () => void;
  chatId?: string;
  worktreeId?: string;
  projectPath?: string;
}

const TABS: { id: WorkspaceTab; label: string; icon: React.ReactNode }[] = [
  { id: "files", label: "Files", icon: <Files className="h-4 w-4" /> },
  { id: "git", label: "Git", icon: <GitBranch className="h-4 w-4" /> },
  { id: "plan", label: "Plan", icon: <ListTodo className="h-4 w-4" /> },
  { id: "packages", label: "Packages", icon: <Terminal className="h-4 w-4" /> },
];

export function MobileWorkspaceSheet({
  isOpen,
  onClose,
  chatId,
  worktreeId,
  projectPath,
}: MobileWorkspaceSheetProps) {
  const [activeTab, setActiveTab] = useState<WorkspaceTab>("files");
  const fileViewerEnabled = useCapability("fileViewer");

  if (!isOpen) return null;

  return createPortal(
    <div
      className="fixed inset-0 z-[9999] flex flex-col bg-background"
      style={{
        paddingTop: "env(safe-area-inset-top)",
        paddingBottom: "env(safe-area-inset-bottom)",
      }}
    >
      <div className="flex min-h-[44px] shrink-0 items-center gap-2 border-b border-border px-2">
        <button
          type="button"
          onClick={onClose}
          className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          aria-label="Close workspace"
        >
          <X className="h-5 w-5" />
        </button>
        <span className="text-sm font-medium">Workspace</span>
      </div>

      <div className="flex shrink-0 border-b border-border">
        {TABS.map((tab) => (
          <button
            key={tab.id}
            type="button"
            onClick={() => setActiveTab(tab.id)}
            className={cn(
              "flex min-h-[44px] flex-1 items-center justify-center gap-1.5 border-b-2 text-xs font-medium",
              activeTab === tab.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground active:bg-muted",
            )}
          >
            {tab.icon}
            {tab.label}
          </button>
        ))}
      </div>

      <div className="min-h-0 flex-1">
        {activeTab === "files" &&
          (fileViewerEnabled ? (
            <MobileFilesPanel worktreeId={worktreeId} />
          ) : (
            <EmptyTab label="Files are unavailable on this surface." />
          ))}

        {activeTab === "git" &&
          (worktreeId ? (
            <div className="overflow-y-auto p-3">
              <GitStatus worktreeId={worktreeId} />
            </div>
          ) : (
            <EmptyTab label="No workspace selected." />
          ))}

        {activeTab === "plan" && (
          <div className="h-full min-h-0">
            <TasksPanel chatId={chatId} />
          </div>
        )}

        {activeTab === "packages" && (
          <MobilePackagesPanel worktreeId={worktreeId} projectPath={projectPath} />
        )}
      </div>
    </div>,
    document.body,
  );
}

function EmptyTab({ label }: { label: string }) {
  return (
    <div className="flex h-full items-center justify-center px-6 text-center text-sm text-muted-foreground">
      {label}
    </div>
  );
}
