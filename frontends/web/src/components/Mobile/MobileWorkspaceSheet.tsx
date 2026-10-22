/**
 * Drill-in for the workspace panels (Files / Git / Plan / Packages) that only
 * exist as desktop sidebar tabs today.
 *
 * Opened two ways: from a chat's header, as context for that chat, and from a
 * workspace group on the chat list, with no chat at all. Without a chat there
 * is no Plan tab. `TasksPanel` falls back to the *active* chat when it gets no
 * chatId, so showing it here would display some other chat's plan under this
 * workspace's name. Git stays read-only either way (`gitManagement` is off).
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
  /** The workspace's name, when opened from somewhere that doesn't already show it. */
  title?: string;
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
  title,
}: MobileWorkspaceSheetProps) {
  const [activeTab, setActiveTab] = useState<WorkspaceTab>("files");
  const fileViewerEnabled = useCapability("fileViewer");
  const tabs = chatId ? TABS : TABS.filter((tab) => tab.id !== "plan");

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
        <span className="truncate text-sm font-medium">{title ?? "Workspace"}</span>
      </div>

      <div className="flex shrink-0 border-b border-border">
        {tabs.map((tab) => (
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

        {activeTab === "plan" && chatId && (
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
