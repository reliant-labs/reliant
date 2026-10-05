import { useEffect, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Folder, RefreshCw, Terminal } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import Card from "@/components/forge-ui/card";
import StatusDot from "@/components/forge-ui/status_dot";
import { Button } from "@/components/ui/Button";
import { Tooltip } from "@/components/ui/Tooltip";
import { projectGrpc } from "@/api/project-grpc";
import { useChatList } from "@/hooks/chat-queries";
import { logger } from "@/lib/logger";
import { cn } from "@/lib/utils";
import { collapseHomePath } from "@/lib/pathUtils";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { useChatNavigationStore } from "@/store/chatNavigationStore";
import { useChatStore } from "@/store/chatStore";
import type { Project } from "@/store/projectStore";
import { useProjectStore } from "@/store/projectStore";
import { useWorktreeStore } from "@/store/worktreeStore";
import type { Chat } from "@/types/chat";

import { describeRemote } from "./projectSource";

/**
 * The current project at a glance: its repository state, how many workspaces
 * it has, and the chats you were last in. Shared by Settings → Projects (under
 * the project table) and the Projects viewer tab (ProjectPanel), so the two
 * places that describe a project describe it the same way.
 *
 * Deliberately compact. The old panel spent a whole card on a four-row
 * worktree status breakdown and another on a "Chat Sessions: N" counter; the
 * facts a reader actually acts on are the branch, whether the tree is dirty,
 * whether it is ahead/behind, and where to go next.
 */
export interface CurrentProjectDetailsProps {
  project: Project;
  /** Called after a recent chat has been selected — e.g. to leave Settings for the chat. */
  onChatOpened?: () => void;
  /** Where "Manage" next to the workspace count goes. Omitted → no link. */
  onManageWorkspaces?: () => void;
  /** Rendered at the right of the panel heading, after the built-in actions. */
  actions?: ReactNode;
  className?: string;
}

const RECENT_CHAT_LIMIT = 5;
const SECTION_LABEL = "text-xs font-semibold uppercase tracking-wide text-muted-foreground";

export function gitInfoQueryKey(projectId: string) {
  return ["projects", projectId, "git-info"] as const;
}

export function CurrentProjectDetails({
  project,
  onChatOpened,
  onManageWorkspaces,
  actions,
  className,
}: CurrentProjectDetailsProps) {
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const selectChat = useChatStore((state) => state.selectChat);
  const navigateToChat = useChatNavigationStore((state) => state.navigateToChat);
  const { data: chats = [] } = useChatList(project.id);

  useEffect(() => {
    void refreshCurrentProject();
    void loadWorktrees(project.id);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- refresh once per project, not per store identity
  }, [project.id]);

  const gitInfo = useQuery({
    queryKey: gitInfoQueryKey(project.id),
    queryFn: () => projectGrpc.getGitInfo(project.id),
    enabled: project.is_git_repo,
    staleTime: 10_000,
    retry: false,
  });

  const activeWorkspaceCount = useMemo(
    () => worktrees.filter((w) => !w.is_main && !w.deleted_at && (!w.project_id || w.project_id === project.id)).length,
    [worktrees, project.id],
  );

  const recentChats = useMemo(
    () =>
      chats
        .filter((chat) => chat.projectId === project.id)
        .sort((a, b) => (Date.parse(b.lastActive) || 0) - (Date.parse(a.lastActive) || 0))
        .slice(0, RECENT_CHAT_LIMIT),
    [chats, project.id],
  );

  const openChat = async (chat: Chat) => {
    const target = chat.worktreeId ? worktrees.find((w) => w.id === chat.worktreeId) ?? null : null;
    // A chat on a workspace whose record we have not loaded keeps the current
    // context rather than dropping the user onto the main checkout.
    if (!chat.worktreeId || target) {
      await switchWorktreeContext(project.id, target);
    }
    navigateToChat(chat.id);
    selectChat(chat);
    onChatOpened?.();
  };

  const canOpenFolder = typeof window !== "undefined" && !!window.electronAPI?.openProjectDirectory;
  const canOpenTerminal = typeof window !== "undefined" && !!window.electronAPI?.openTerminal;

  const openFolder = async () => {
    const result = await window.electronAPI?.openProjectDirectory?.(project.path);
    if (result && !result.success) logger.error("Failed to open project directory:", result.error);
  };
  const openTerminal = async () => {
    const result = await window.electronAPI?.openTerminal?.(project.path);
    if (result && !result.success) logger.error("Failed to open terminal:", result.error);
  };

  return (
    <Card padding="none" className={cn("bg-card", className)} data-testid="current-project-details">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="truncate text-sm font-semibold text-foreground">{project.name}</h2>
            <Badge label="Current" variant="info" size="sm" />
          </div>
          <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground" title={project.path}>
            {collapseHomePath(project.path)}
          </p>
        </div>
        <div className="flex flex-shrink-0 items-center gap-1.5">
          {canOpenFolder && (
            <Button variant="secondary" size="sm" leftIcon={<Folder className="h-3.5 w-3.5" />} onClick={() => void openFolder()}>
              Open folder
            </Button>
          )}
          {canOpenTerminal && (
            <Button variant="secondary" size="sm" leftIcon={<Terminal className="h-3.5 w-3.5" />} onClick={() => void openTerminal()}>
              Terminal
            </Button>
          )}
          {actions}
        </div>
      </div>

      <div className="grid grid-cols-1 divide-y divide-border md:grid-cols-2 md:divide-x md:divide-y-0">
        <section aria-labelledby={`repo-${project.id}`} className="flex flex-col gap-3 px-4 py-3">
          <div className="flex items-center justify-between gap-2">
            <h3 id={`repo-${project.id}`} className={SECTION_LABEL}>
              Repository
            </h3>
            {project.is_git_repo && (
              <Tooltip content="Refresh git status" placement="left" delay={300}>
                <button
                  type="button"
                  aria-label="Refresh git status"
                  onClick={() => void gitInfo.refetch()}
                  disabled={gitInfo.isFetching}
                  className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
                >
                  <RefreshCw className={cn("h-3.5 w-3.5", gitInfo.isFetching && "animate-spin motion-reduce:animate-none")} />
                </button>
              </Tooltip>
            )}
          </div>

          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 text-sm">
            {project.is_git_repo ? (
              <GitFacts
                project={project}
                loading={gitInfo.isPending}
                failed={gitInfo.isError}
                info={gitInfo.data}
              />
            ) : (
              <Fact label="Git">
                <span className="text-muted-foreground">Not a git repository</span>
              </Fact>
            )}
            <Fact label="Workspaces">
              <span className="flex items-center gap-2">
                <span className="tabular-nums text-foreground">
                  {activeWorkspaceCount === 0 ? "None" : `${activeWorkspaceCount} active`}
                </span>
                {onManageWorkspaces && (
                  <button
                    type="button"
                    onClick={onManageWorkspaces}
                    className="rounded-sm text-xs text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    Manage
                  </button>
                )}
              </span>
            </Fact>
          </dl>
        </section>

        <section aria-labelledby={`chats-${project.id}`} className="flex min-w-0 flex-col gap-2 px-4 py-3">
          <h3 id={`chats-${project.id}`} className={SECTION_LABEL}>
            Recent chats
          </h3>
          {recentChats.length === 0 ? (
            <p className="text-sm text-muted-foreground">No chats in this project yet.</p>
          ) : (
            <ul className="-mx-2 flex flex-col">
              {recentChats.map((chat) => (
                <li key={chat.id}>
                  <button
                    type="button"
                    onClick={() => void openChat(chat)}
                    className="flex w-full items-baseline gap-3 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <span className="min-w-0 flex-1 truncate text-foreground">{chat.title || "Untitled chat"}</span>
                    <time
                      dateTime={chat.lastActive}
                      title={formatAbsoluteTime(chat.lastActive)}
                      className="flex-shrink-0 text-xs tabular-nums text-muted-foreground"
                    >
                      {formatRelativeTime(chat.lastActive)}
                    </time>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </Card>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

interface GitFactsProps {
  project: Project;
  loading: boolean;
  failed: boolean;
  info: Awaited<ReturnType<typeof projectGrpc.getGitInfo>> | undefined;
}

function GitFacts({ project, loading, failed, info }: GitFactsProps) {
  const remoteUrl = info?.remote_url || project.remote_url;
  const remote = remoteUrl ? describeRemote(remoteUrl) : null;

  const branch = info?.current_branch || (loading || failed ? project.default_branch : undefined);
  const changeCount = info
    ? (info.staged_files?.length ?? 0) + (info.unstaged_files?.length ?? 0) + (info.untracked_files?.length ?? 0)
    : 0;

  return (
    <>
      <Fact label="Branch">
        <span className="block truncate font-mono text-foreground">{branch || "Detached HEAD"}</span>
      </Fact>
      <Fact label="Working tree">
        {loading ? (
          <span className="text-muted-foreground">Checking…</span>
        ) : failed || !info ? (
          // The daemon reads git; when it is offline there is nothing to show,
          // and saying so beats a spinner that never resolves.
          <span className="text-muted-foreground">Unavailable — is your machine connected?</span>
        ) : changeCount === 0 ? (
          <StatusDot variant="active" size="sm" label="Clean" />
        ) : (
          <StatusDot
            variant="warning"
            size="sm"
            label={`${changeCount} uncommitted ${changeCount === 1 ? "change" : "changes"}`}
          />
        )}
      </Fact>
      {info && remote && (info.ahead > 0 || info.behind > 0) && (
        <Fact label="Sync">
          <span className="tabular-nums text-foreground">
            {[info.ahead > 0 && `${info.ahead} ahead`, info.behind > 0 && `${info.behind} behind`]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </Fact>
      )}
      {remote && (
        <Fact label="Remote">
          {remote.href ? (
            <a
              href={remote.href}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex max-w-full items-center gap-1 rounded-sm font-mono text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="truncate">{remote.repo}</span>
              <ExternalLink className="h-3 w-3 flex-shrink-0" aria-hidden="true" />
              <span className="sr-only">(opens in a new tab)</span>
            </a>
          ) : (
            <span className="block truncate font-mono text-foreground" title={remoteUrl}>
              {remote.repo}
            </span>
          )}
        </Fact>
      )}
    </>
  );
}
