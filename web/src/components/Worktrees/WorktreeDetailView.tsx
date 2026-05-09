import { useState, useRef, useEffect, useMemo } from "react";
import {
  Archive,
  Check,
  ChevronDown,
  Clock,
  Copy,
  ExternalLink,
  FolderGit2,
  FolderOpen,
  GitMerge,
  GitPullRequest,
  Loader2,
  MessageSquare,
  Plus,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { usePreferences } from "../../hooks/settings-queries";
import { useWindowContext } from "../../hooks/useWindowContext";
import { GitStatus } from "../Git/GitStatus";
import { CommitHistory } from "../Git/CommitHistory";
import { Button } from "../ui/Button";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { workspaceButton, workspaceIconButton } from "./workspaceStyles";

export function WorktreeDetailView() {
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const deleteWorktree = useWorktreeStore((state) => state.deleteWorktree);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const updateWorktreeStatus = useWorktreeStore((state) => state.updateWorktreeStatus);
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const { data: preferences } = usePreferences();
  const { isElectron, openInNewWindow } = useWindowContext();
  const [copiedPath, setCopiedPath] = useState(false);
  const [statusDropdownOpen, setStatusDropdownOpen] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setStatusDropdownOpen(false);
      }
    };

    if (statusDropdownOpen) {
      document.addEventListener("mousedown", handleClickOutside);
    }

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [statusDropdownOpen]);

  const associatedChatCount = useMemo(() => {
    if (!currentWorktree) return 0;
    return chats.filter((chat) => chat.worktreeId === currentWorktree.id).length;
  }, [chats, currentWorktree]);

  const handleStatusChange = async (newStatus: Worktree["status"]) => {
    if (currentWorktree && newStatus !== currentWorktree.status) {
      await updateWorktreeStatus(currentWorktree.id, newStatus);
      setStatusDropdownOpen(false);
    }
  };

  const statusOptions: Array<{ value: Worktree["status"]; label: string; description: string }> = [
    { value: WorktreeStatus.ACTIVE, label: "Active", description: "Currently being worked on" },
    { value: WorktreeStatus.MERGING, label: "Merging", description: "PR in review or merging" },
    { value: WorktreeStatus.COMPLETED, label: "Completed", description: "Work finished and merged" },
    { value: WorktreeStatus.ABANDONED, label: "Abandoned", description: "No longer working on this" },
  ];

  if (!currentWorktree) {
    return (
      <div className="flex h-full items-center justify-center p-8 text-center">
        <div className="max-w-md rounded-2xl border border-dashed border-border/70 p-8">
          <FolderGit2 className="mx-auto mb-4 h-12 w-12 text-muted-foreground/40" />
          <h3 className="mb-2 text-lg font-semibold text-foreground">No workspace selected</h3>
          <p className="text-sm text-muted-foreground">
            Select a workspace to review details, status, recent commits, and lifecycle actions.
          </p>
        </div>
      </div>
    );
  }

  const isDeleting = deletingId === currentWorktree.id;

  const getStatusColor = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return "text-status-active";
      case WorktreeStatus.COMPLETED:
        return "text-status-completed";
      case WorktreeStatus.ABANDONED:
        return "text-warning";
      case WorktreeStatus.MERGING:
        return "text-status-merging";
      default:
        return "text-muted-foreground";
    }
  };

  const getStatusIcon = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.MERGING:
        return <GitMerge className="h-4 w-4" />;
      case WorktreeStatus.COMPLETED:
        return <GitPullRequest className="h-4 w-4" />;
      default:
        return <FolderGit2 className="h-4 w-4" />;
    }
  };

  const getStatusLabel = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return "Active";
      case WorktreeStatus.COMPLETED:
        return "Completed";
      case WorktreeStatus.ABANDONED:
        return "Abandoned";
      case WorktreeStatus.MERGING:
        return "Merging";
      default:
        return "Unknown";
    }
  };

  const formatDate = (date: string) => {
    if (!date) return null;
    try {
      const parsedDate = new Date(date);
      if (isNaN(parsedDate.getTime())) return null;
      return parsedDate.toLocaleDateString("en-US", {
        weekday: "short",
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      });
    } catch {
      return null;
    }
  };

  const handleArchive = () => {
    const mode = preferences?.worktree.archiveMode ?? "ask_me";
    if (mode === "ask_me") {
      setDeleteModalOpen(true);
      return;
    }

    const options =
      mode === "always_cleanup"
        ? {
            deleteGitBranch: preferences?.worktree.defaultDeleteBranch ?? false,
            deleteLocalDirectory: preferences?.worktree.defaultDeleteDirectory ?? true,
          }
        : {
            deleteGitBranch: false,
            deleteLocalDirectory: false,
          };

    deleteWorktree(currentWorktree.id, options);
  };

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(currentWorktree.path);
      setCopiedPath(true);
      setTimeout(() => setCopiedPath(false), 2000);
    } catch (error) {
      console.error("Failed to copy path:", error);
    }
  };

  const detailItems = [
    { label: "Branch", value: currentWorktree.branch },
    { label: "Base", value: currentWorktree.base_branch || currentProject?.default_branch || "Not set" },
    { label: "Chats", value: `${associatedChatCount}` },
    { label: "Created", value: formatDate(currentWorktree.created_at) },
    { label: "Last active", value: formatDate(currentWorktree.last_active) },
    { label: "ID", value: currentWorktree.id },
    { label: "Session", value: currentWorktree.session_id },
  ].filter((item): item is { label: string; value: string } => Boolean(item.value));

  return (
    <div className="flex h-full flex-col bg-background">
      <div className="flex-shrink-0 border-b border-border/60 bg-card/20 px-6 py-5">
        <div className="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
          <div className="min-w-0">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <div className={cn("flex items-center gap-2", getStatusColor(currentWorktree.status))}>
                {getStatusIcon(currentWorktree.status)}
                <h2 className="truncate text-2xl font-semibold tracking-tight text-foreground">
                  {currentWorktree.name}
                </h2>
              </div>
              <span className={cn("rounded-full border px-2.5 py-1 text-xs font-medium", getStatusColor(currentWorktree.status), "bg-current/10 border-current/20")}>
                {getStatusLabel(currentWorktree.status)}
              </span>
              {currentWorktree.is_main && (
                <span className="rounded-full border border-border bg-background px-2.5 py-1 text-xs font-medium text-muted-foreground">
                  Main workspace
                </span>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <FolderGit2 className="h-3.5 w-3.5" />
                {currentWorktree.branch}
              </span>
              {currentWorktree.base_branch && <span>based on {currentWorktree.base_branch}</span>}
              {formatDate(currentWorktree.last_active) && (
                <span className="flex items-center gap-1.5">
                  <Clock className="h-3.5 w-3.5" />
                  Last active {formatDate(currentWorktree.last_active)}
                </span>
              )}
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <div className="relative" ref={dropdownRef}>
              <Button
                onClick={() => setStatusDropdownOpen(!statusDropdownOpen)}
                rightIcon={<ChevronDown className="h-3 w-3" />}
                variant="outline"
                size="sm"
                className={workspaceButton.secondary}
              >
                Status
              </Button>

              {statusDropdownOpen && (
                <div className="absolute right-0 top-full z-50 mt-2 w-64 overflow-hidden rounded-xl border border-border bg-background shadow-lg">
                  {statusOptions.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      onClick={() => handleStatusChange(option.value)}
                      className={cn(
                        "w-full border-b border-border/50 px-3 py-2.5 text-left transition-colors last:border-b-0",
                        currentWorktree.status === option.value
                          ? "bg-primary/10"
                          : "hover:bg-muted/60"
                      )}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-sm font-medium text-foreground">{option.label}</span>
                        {currentWorktree.status === option.value && (
                          <Check className="h-3.5 w-3.5 text-primary" />
                        )}
                      </div>
                      <div className="mt-0.5 text-xs text-muted-foreground">{option.description}</div>
                    </button>
                  ))}
                </div>
              )}
            </div>

            {isElectron && (
              <Button
                onClick={async () => {
                  const result = await openInNewWindow(currentWorktree);
                  if (!result) {
                    alert("Failed to open worktree in new window. Please try again.");
                  }
                }}
                leftIcon={<Plus className="h-3 w-3" />}
                variant="secondary"
                size="sm"
                className={workspaceButton.secondary}
                title="Open in new window"
              >
                New Window
              </Button>
            )}

            {currentWorktree.session_id && (
              <Button
                onClick={() => {
                  // Navigate to session
                }}
                leftIcon={<ExternalLink className="h-3 w-3" />}
                variant="outline"
                size="sm"
                className={workspaceButton.subtle}
              >
                Open Session
              </Button>
            )}

            <Button
              onClick={handleArchive}
              leftIcon={isDeleting ? <Loader2 className="h-3 w-3 animate-spin" /> : <Archive className="h-3 w-3" />}
              variant="outline"
              size="sm"
              disabled={isDeleting || currentWorktree.is_main}
              className={workspaceButton.warning}
              title={currentWorktree.is_main ? "Cannot archive the main workspace" : "Archive workspace"}
            >
              {isDeleting ? "Archiving…" : "Archive"}
            </Button>
          </div>
        </div>

        <div className="mt-4 rounded-xl border border-border/60 bg-background/80 px-3 py-2">
          <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
            <FolderOpen className="h-3.5 w-3.5 flex-shrink-0" />
            <span className="flex-shrink-0 font-medium">Path</span>
            <span className="min-w-0 flex-1 truncate font-mono text-foreground">
              {currentWorktree.path}
            </span>
            <button
              type="button"
              onClick={copyPath}
              className={cn(workspaceIconButton, "h-6 w-6 border-border/60 shadow-none")}
              title="Copy path to clipboard"
            >
              {copiedPath ? <Check className="h-3.5 w-3.5 text-success" /> : <Copy className="h-3.5 w-3.5" />}
            </button>
          </div>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-6">
        <div className="mx-auto grid max-w-5xl gap-4 xl:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-4">
            <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
              <div className="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h3 className="text-sm font-semibold text-foreground">Git status</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Read-only status for this workspace.
                  </p>
                </div>
              </div>
              <GitStatus worktreeId={currentWorktree.id} />
            </section>

            <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
              <div className="mb-4 flex items-center justify-between gap-3">
                <div>
                  <h3 className="text-sm font-semibold text-foreground">Recent commits</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Commit history is shown for context only.
                  </p>
                </div>
              </div>
              <CommitHistory worktreeId={currentWorktree.id} limit={10} initialDisplay={3} />
            </section>
          </div>

          <aside className="space-y-4">
            <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
              <h3 className="text-sm font-semibold text-foreground">Workspace details</h3>
              <dl className="mt-4 space-y-3">
                {detailItems.map((item) => (
                  <div key={item.label} className="space-y-1">
                    <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      {item.label}
                    </dt>
                    <dd className="break-words text-sm text-foreground">
                      {item.value}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>

            <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
              <div className="flex items-start gap-3">
                <div className="rounded-lg bg-muted p-2 text-muted-foreground">
                  <MessageSquare className="h-4 w-4" />
                </div>
                <div>
                  <h3 className="text-sm font-semibold text-foreground">Associated chats</h3>
                  <p className="mt-1 text-sm text-muted-foreground">
                    {associatedChatCount === 0
                      ? "No chats are linked to this workspace."
                      : `${associatedChatCount} chat${associatedChatCount === 1 ? "" : "s"} linked to this workspace.`}
                  </p>
                </div>
              </div>
            </section>
          </aside>
        </div>
      </div>

      <DeleteWorktreeModal
        isOpen={deleteModalOpen}
        onClose={() => setDeleteModalOpen(false)}
        worktree={currentWorktree}
        chatCount={associatedChatCount}
        onConfirmDelete={(options) => {
          deleteWorktree(currentWorktree.id, options);
        }}
      />
    </div>
  );
}