import { useEffect, useMemo, useRef, useState } from "react";
import {
  AppWindow,
  Archive,
  Check,
  ChevronDown,
  ChevronLeft,
  Copy,
  FolderGit2,
  FolderOpen,
  Loader2,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { toast } from "../../lib/toast-manager";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { useWindowContext } from "../../hooks/useWindowContext";
import { GitStatus } from "../Git/GitStatus";
import { CommitHistory } from "../Git/CommitHistory";
import { Button } from "../ui/Button";
import { Tooltip } from "../ui/Tooltip";
import Badge from "../forge-ui/badge";
import StatusDot from "../forge-ui/status_dot";
import Card, { CardHeader, CardInset } from "../forge-ui/card";
import EmptyState from "../forge-ui/empty_state";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import {
  LIFECYCLE_OPTIONS,
  chatsForWorkspace,
  hasCheckout,
  lifecycleOf,
  workingTreeState,
} from "./workspaceStatus";
import { useWorkspaceGitStatus } from "./useWorkspaceGitStatus";
import { useArchiveWorkspace, useOpenWorkspace } from "./useWorkspaceActions";
import { IconAction, TimeCell } from "./WorkspaceTableParts";

interface WorktreeDetailViewProps {
  /**
   * Which workspace to show. Omitted, it follows the app's current workspace
   * (the Workspaces viewer tab).
   */
  worktreeId?: string;
  /** Renders a "back to the list" breadcrumb when set. */
  onBack?: () => void;
  /** Called after Open / a chat link has switched the app into the workspace. */
  onOpened?: () => void;
  /**
   * Render inside a host that already scrolls and pads (Settings). Without it
   * the view owns a full-height scroll area, as in the viewer tab.
   */
  embedded?: boolean;
}

/** How many linked chats to list before summarising the rest. */
const CHAT_LIST_LIMIT = 6;

/**
 * One workspace in full: what branch it is on and whether its working tree is
 * clean, the commits it has made, where it lives on disk, the chats running in
 * it, and the lifecycle actions (set status, open, archive).
 */
export function WorktreeDetailView({
  worktreeId,
  onBack,
  onOpened,
  embedded = false,
}: WorktreeDetailViewProps = {}) {
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const worktree = worktreeId
    ? worktrees.find((candidate) => candidate.id === worktreeId) ?? null
    : currentWorktree;

  if (!worktree) {
    return (
      <div className="forge-ui flex h-full items-center justify-center p-6">
        <div className="w-full max-w-md">
          <EmptyState
            icon={<FolderGit2 className="h-6 w-6" />}
            title="No workspace selected"
            description="Pick a workspace to see its branch, uncommitted changes, recent commits and the chats working in it."
          />
        </div>
      </div>
    );
  }

  return (
    <WorkspaceDetail worktree={worktree} onBack={onBack} onOpened={onOpened} embedded={embedded} />
  );
}

function WorkspaceDetail({
  worktree,
  onBack,
  onOpened,
  embedded,
}: {
  worktree: Worktree;
  onBack?: () => void;
  onOpened?: () => void;
  embedded: boolean;
}) {
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const currentWorktreeId = useWorktreeStore((state) => state.currentWorktree?.id);
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const { isElectron, openInNewWindow } = useWindowContext();
  const checkedOut = hasCheckout(worktree);
  const { data: gitStatus } = useWorkspaceGitStatus(worktree.id, checkedOut);
  const archive = useArchiveWorkspace();
  const { openWorkspace, openChat } = useOpenWorkspace(onOpened);
  const [copiedPath, setCopiedPath] = useState(false);

  const linkedChats = useMemo(() => chatsForWorkspace(chats, worktree.id), [chats, worktree.id]);
  const isArchiving = deletingId === worktree.id;
  const isCurrent = currentWorktreeId === worktree.id;
  const treeState = gitStatus ? workingTreeState(gitStatus) : null;
  const baseBranch = worktree.base_branch || currentProject?.default_branch || "";

  const copyPath = async () => {
    try {
      await navigator.clipboard.writeText(worktree.path);
      setCopiedPath(true);
      setTimeout(() => setCopiedPath(false), 2000);
    } catch (error) {
      console.error("Failed to copy path:", error);
      toast.error("Couldn't copy the path to the clipboard");
    }
  };

  const openWindow = async () => {
    const opened = await openInNewWindow(worktree);
    if (!opened) toast.error("Couldn't open the workspace in a new window");
  };

  return (
    <div
      className={cn("forge-ui bg-background", !embedded && "h-full overflow-y-auto")}
      data-testid="workspace-detail"
    >
      <div className={cn("flex w-full flex-col gap-5", !embedded && "mx-auto max-w-5xl px-6 py-6")}>
          {onBack && (
            <nav aria-label="Breadcrumb" className="flex items-center gap-1 text-xs text-muted-foreground">
              <button
                type="button"
                onClick={onBack}
                className="inline-flex items-center gap-1 rounded-sm hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
                Workspaces
              </button>
              <span aria-hidden="true">/</span>
              <span className="truncate text-foreground" aria-current="page">
                {worktree.name}
              </span>
            </nav>
          )}

          <header className="flex flex-col gap-4 border-b border-border pb-5 sm:flex-row sm:items-start sm:justify-between">
            <div className="flex min-w-0 flex-col gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <h2 className="min-w-0 truncate text-lg font-semibold tracking-tight text-foreground text-balance">
                  {worktree.name}
                </h2>
                {worktree.is_main && <Badge label="Main checkout" size="sm" />}
                {isCurrent && <Badge label="Current" variant="info" size="sm" />}
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
                <span className="font-mono text-foreground">{worktree.branch}</span>
                {baseBranch && baseBranch !== worktree.branch && (
                  <span>
                    from <span className="font-mono">{baseBranch}</span>
                  </span>
                )}
                {treeState && (
                  <Tooltip content={treeState.detail} delay={300} wrapperClassName="inline-flex">
                    <StatusDot variant={treeState.variant} label={treeState.label} size="sm" />
                  </Tooltip>
                )}
              </div>
            </div>

            <div className="flex flex-shrink-0 flex-wrap items-center gap-2">
              <LifecycleMenu worktree={worktree} />
              {isElectron && (
                <Button
                  variant="outline"
                  size="sm"
                  leftIcon={<AppWindow className="h-3.5 w-3.5" />}
                  onClick={() => void openWindow()}
                >
                  New window
                </Button>
              )}
              <Tooltip
                content={
                  worktree.is_main
                    ? "The main checkout can't be archived"
                    : "Archive this workspace and its chats. You can restore it later."
                }
                delay={300}
                wrapperClassName="inline-flex"
              >
                <Button
                  variant="outline"
                  size="sm"
                  leftIcon={
                    isArchiving ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    ) : (
                      <Archive className="h-3.5 w-3.5" />
                    )
                  }
                  onClick={() => archive.requestArchive(worktree)}
                  disabled={worktree.is_main || isArchiving}
                >
                  {isArchiving ? "Archiving…" : "Archive"}
                </Button>
              </Tooltip>
              <Button
                variant="primary"
                size="sm"
                leftIcon={<FolderOpen className="h-3.5 w-3.5" />}
                onClick={() => void openWorkspace(worktree)}
              >
                Open workspace
              </Button>
            </div>
          </header>

          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
            <div className="flex min-w-0 flex-col gap-4">
              {checkedOut ? (
                <>
                  <Card>
                    <CardHeader
                      title="Working tree"
                      description="Uncommitted changes in this workspace, and how far it is from its upstream branch."
                    />
                    <CardInset padding="md">
                      <GitStatus worktreeId={worktree.id} />
                    </CardInset>
                  </Card>

                  <Card>
                    <CardHeader
                      title="Commits on this branch"
                      description={
                        baseBranch
                          ? `Commits made here that aren't on ${baseBranch} yet.`
                          : "Commits made on this branch."
                      }
                    />
                    <CardInset padding="md">
                      <CommitHistory worktreeId={worktree.id} limit={10} initialDisplay={3} />
                    </CardInset>
                  </Card>
                </>
              ) : (
                <Card>
                  <CardHeader title={lifecycleOf(worktree.status).label} />
                  <p className="text-pretty text-sm text-muted-foreground">
                    {worktree.status === WorktreeStatus.FAILED
                      ? "Creating this workspace's worktree did not finish, so there is no checkout to show. Archive it and branch the chat again to retry."
                      : "Your machine is still creating this workspace's worktree. Its git status and commits appear here once the checkout exists."}
                  </p>
                </Card>
              )}
            </div>

            <div className="flex min-w-0 flex-col gap-4">
              <Card>
                <CardHeader title="Details" />
                <dl className="flex flex-col gap-3 text-sm">
                  <div className="flex flex-col gap-1">
                    <dt className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                      Location
                    </dt>
                    <dd>
                      <CardInset className="flex min-w-0 items-center gap-1 py-1 pl-2 pr-1">
                        <span
                          className="min-w-0 flex-1 truncate font-mono text-xs text-foreground"
                          title={worktree.path}
                        >
                          {worktree.path}
                        </span>
                        <IconAction
                          label="Copy path"
                          hint={copiedPath ? "Copied" : "Copy path to clipboard"}
                          icon={
                            copiedPath ? (
                              <Check className="h-3.5 w-3.5 text-success-ink" />
                            ) : (
                              <Copy className="h-3.5 w-3.5" />
                            )
                          }
                          onClick={() => void copyPath()}
                        />
                      </CardInset>
                    </dd>
                  </div>
                  <DetailRow label="Base branch" mono>
                    {baseBranch || "Not set"}
                  </DetailRow>
                  <DetailRow label="Created">
                    <TimeCell value={worktree.created_at} empty="Unknown" />
                  </DetailRow>
                  <DetailRow label="Last active">
                    <TimeCell value={worktree.last_active} />
                  </DetailRow>
                  <DetailRow label="ID" mono>
                    <span className="break-all text-xs">{worktree.id}</span>
                  </DetailRow>
                </dl>
              </Card>

              <Card>
                <CardHeader
                  title="Chats"
                  description={
                    linkedChats.length === 0
                      ? undefined
                      : `${linkedChats.length} chat${linkedChats.length === 1 ? "" : "s"} working in this workspace.`
                  }
                />
                {linkedChats.length === 0 ? (
                  <p className="text-pretty text-xs text-muted-foreground">
                    No chats are using this workspace. Start a chat while it is open, or branch an
                    existing one into it.
                  </p>
                ) : (
                  <ul className="-mx-1 flex flex-col">
                    {linkedChats.slice(0, CHAT_LIST_LIMIT).map((chat) => (
                      <li key={chat.id}>
                        <button
                          type="button"
                          onClick={() => void openChat(chat)}
                          aria-label={`Open chat ${chat.title || "Untitled chat"}`}
                          className="flex w-full min-w-0 items-center justify-between gap-2 rounded-md px-1 py-1.5 text-left hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <span className="min-w-0 truncate text-sm text-foreground">
                            {chat.title || "Untitled chat"}
                          </span>
                          <span className="flex-shrink-0">
                            <TimeCell value={chat.lastMessageAt || chat.updatedAt} empty="" />
                          </span>
                        </button>
                      </li>
                    ))}
                    {linkedChats.length > CHAT_LIST_LIMIT && (
                      <li className="px-1 pt-1 text-xs text-muted-foreground">
                        and {linkedChats.length - CHAT_LIST_LIMIT} more in the sidebar
                      </li>
                    )}
                  </ul>
                )}
              </Card>
            </div>
          </div>
      </div>

      <DeleteWorktreeModal
        key={archive.pending?.id ?? "none"}
        isOpen={archive.pending !== null}
        onClose={archive.cancel}
        worktree={archive.pending}
        chatCount={linkedChats.length}
        onConfirmDelete={archive.confirm}
      />
    </div>
  );
}

function DetailRow({
  label,
  mono,
  children,
}: {
  label: string;
  mono?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="flex-shrink-0 text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("min-w-0 text-right text-foreground", mono && "font-mono text-xs")}>
        {children}
      </dd>
    </div>
  );
}

/**
 * Where the work in this workspace stands (Active / Merging / Completed /
 * Abandoned). It is the user's own label: nothing acts on it automatically.
 */
function LifecycleMenu({ worktree }: { worktree: Worktree }) {
  const updateWorktreeStatus = useWorktreeStore((state) => state.updateWorktreeStatus);
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const current = lifecycleOf(worktree.status);

  useEffect(() => {
    if (!open) return;
    const handlePointer = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setOpen(false);
    };
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", handlePointer);
    document.addEventListener("keydown", handleKey);
    return () => {
      document.removeEventListener("mousedown", handlePointer);
      document.removeEventListener("keydown", handleKey);
    };
  }, [open]);

  return (
    <div className="relative" ref={menuRef}>
      <Button
        variant="outline"
        size="sm"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Status: ${current.label}. Change status`}
        onClick={() => setOpen((value) => !value)}
        rightIcon={<ChevronDown className="h-3 w-3" />}
      >
        <StatusDot variant={current.variant} size="sm" />
        {current.label}
      </Button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 top-full z-50 mt-1 w-64 overflow-hidden rounded-md border border-border bg-popover py-1 text-popover-foreground shadow-lg"
        >
          <p className="px-3 pb-1 pt-1.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
            Mark this workspace as
          </p>
          {LIFECYCLE_OPTIONS.map((value) => {
            const option = lifecycleOf(value);
            const selected = worktree.status === value;
            return (
              <button
                key={value}
                type="button"
                role="menuitemradio"
                aria-checked={selected}
                onClick={async () => {
                  if (!selected) await updateWorktreeStatus(worktree.id, value);
                  setOpen(false);
                }}
                className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-muted focus:bg-muted focus:outline-none"
              >
                <span className="mt-1.5">
                  <StatusDot variant={option.variant} size="sm" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block text-sm text-foreground">{option.label}</span>
                  <span className="block text-xs text-muted-foreground">{option.description}</span>
                </span>
                {selected && <Check className="mt-1 h-3.5 w-3.5 text-primary" aria-hidden="true" />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
