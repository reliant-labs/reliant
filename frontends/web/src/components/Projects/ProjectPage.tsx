// Copyright (c) 2025 Reliant Labs

/**
 * A project's page (`/o/<org>/projects/<id>`, control-plane
 * docs/design/unified-shell-and-projects.md §8.4, R6, R7, R9): what the
 * project is and what it runs.
 *
 *   Overview     Apps — each app its code holds, into its console, with its
 *                environments and last deploy; Code — the repos with their
 *                remote and the forge.yaml apps found in each, and the
 *                machines it is checked out on; Chats — your recent chats in
 *                it, and "New chat" there.
 *   Workspaces   the workspaces (git worktrees) its chats work in — Settings
 *                → Workspaces' own content (WorkspacesBody).
 *   Settings     the per-project controls of Settings → Projects: rename,
 *                hide, remove, its machines, and the move to another org.
 *
 * The project comes from the org's catalog (useProjectCatalog), the same
 * answer the Projects list shows, so a project of another org is never shown
 * under this one (§3.4): it reads as "not in this organization". A project
 * shared with the person has the viewer's shape: no folder, no checkouts, no
 * chats of the owner's, and no Workspaces or Settings.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { AlertTriangle, ExternalLink, FolderGit2, Globe, HardDrive, MessageSquare, SquarePen, Users } from "lucide-react";

import AlertBanner from "@/components/forge-ui/alert_banner";
import Button from "@/components/forge-ui/button";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { useChatList } from "@/hooks/chat-queries";
import { useProjectCatalog } from "@/hooks/project-catalog-queries";
import { useSidebarProjectPrefs } from "@/hooks/sidebar-project-queries";
import { useActiveOrg } from "@/hooks/useOrgs";
import { logger } from "@/lib/logger";
import { appPath, projectChatsPath, projectPath, projectsPath, type ProjectPageTab } from "@/lib/paths";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";
import { linkLabel, type AppChipModel, type CatalogCheckout, type ProjectCatalogRow } from "@/services/projects/catalog";
import { openChat, openNewChat } from "@/store/openChat";
import { useProjectStore, type Project } from "@/store/projectStore";
import type { Chat } from "@/types/chat";

import { AreaShell } from "../Layout/AreaShell";
import { WorkspacesBody } from "../Settings/BranchesSection";
import { MoveProjectOrgDialog } from "../Settings/projects/MoveProjectOrgDialog";
import { ProjectMachines } from "../Settings/projects/ProjectMachines";
import { ProjectMoveSection } from "../Settings/projects/ProjectMoveSection";
import { ProjectsTable } from "../Settings/projects/ProjectsTable";
import { AppChip } from "./AppChip";
import { checkoutText } from "./ProjectRowSummary";
import { RemoveProjectModal } from "./RemoveProjectModal";

type Tab = "overview" | Extract<ProjectPageTab, "settings" | "workspaces">;

export function ProjectPage() {
  return <ProjectPageShell tab="overview" />;
}

export function ProjectSettingsPage() {
  return <ProjectPageShell tab="settings" />;
}

export function ProjectWorkspacesPage() {
  return <ProjectPageShell tab="workspaces" />;
}

function ProjectPageShell({ tab }: { tab: Tab }) {
  return (
    <AreaShell areaPath={projectsPath()} areaLabel="Projects" areaNoun="projects">
      <ProjectPageBody tab={tab} />
    </AreaShell>
  );
}

export function ProjectPageBody({ tab }: { tab: Tab }) {
  const { projectId = "" } = useParams({ strict: false }) as { projectId?: string };
  const { catalog, isLoading, error, refetch } = useProjectCatalog();
  const row = useMemo(
    () => catalog?.rows.find((r): r is ProjectCatalogRow => r.kind === "project" && r.key === projectId),
    [catalog, projectId],
  );

  let body: ReactNode;
  if (isLoading && !catalog) {
    body = (
      <div className="rounded-xl border border-border bg-card" aria-busy="true" data-testid="project-page-loading">
        <SkeletonLoader variant="list-item" count={3} />
      </div>
    );
  } else if (error && !catalog) {
    body = (
      <AlertBanner
        variant="error"
        title="Couldn’t load this project"
        message={error.message || "The server did not answer."}
        action={{ label: "Try again", onClick: refetch }}
      />
    );
  } else if (!row) {
    body = <NotInThisOrg />;
  } else {
    // A viewer of a shared project gets its read-only overview only.
    const shown: Tab = row.shared ? "overview" : tab;
    body = (
      <>
        <ProjectHeader row={row} tab={shown} />
        {shown === "overview" && <ProjectOverview row={row} hostedAppsState={catalog?.hostedAppsState ?? "ok"} />}
        {shown === "workspaces" && <ProjectWorkspaces project={row.project} />}
        {shown === "settings" && <ProjectSettings project={row.project} />}
      </>
    );
  }
  return (
    <div className="forge-ui flex flex-col gap-5" data-testid="project-page" data-project-id={projectId}>
      {body}
    </div>
  );
}

function NotInThisOrg() {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed border-border px-6 py-14 text-center" data-testid="project-not-found">
      <FolderGit2 className="h-6 w-6 text-muted-foreground" aria-hidden="true" />
      <h1 className="text-section text-foreground">No such project in this organization</h1>
      <p className="max-w-md text-ui text-muted-foreground">
        It may have been removed, moved to another organization, or never shared with you. Switch organization at the top right, or pick it from Projects.
      </p>
      <Button variant="secondary" size="sm" href={projectsPath()}>
        All projects
      </Button>
    </div>
  );
}

// ── Header and tabs ─────────────────────────────────────────────────────────

const TABS: { id: Tab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "workspaces", label: "Workspaces" },
  { id: "settings", label: "Settings" },
];

/** "New chat in this project": the new-chat view, in this project, on the chat home filtered to it. */
function useNewChatInProject(projectId: string): () => void {
  const navigate = useNavigate();
  return () => {
    void openNewChat({ projectId })
      .then(() => navigate({ to: projectChatsPath(projectId) }))
      .catch((err: unknown) => {
        logger.error("[ProjectPage] Failed to start a chat", err);
        toast.error("Could not start a chat in this project");
      });
  };
}

function ProjectHeader({ row, tab }: { row: ProjectCatalogRow; tab: Tab }) {
  const { active } = useActiveOrg();
  const newChat = useNewChatInProject(row.key);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex min-w-0 items-center gap-2">
            <FolderGit2 className="h-5 w-5 shrink-0 text-muted-foreground" aria-hidden="true" />
            <h1 className="truncate text-title text-foreground" data-testid="project-page-name">
              {row.name}
            </h1>
            {row.shared && (
              <span className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-meta text-muted-foreground" data-testid="project-shared-badge">
                <Users className="h-3 w-3" aria-hidden="true" />
                Shared
              </span>
            )}
          </div>
          <p className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-meta text-muted-foreground">
            {active?.name && <span>{active.name}</span>}
            {active?.name && <span aria-hidden>·</span>}
            <span>{row.shared ? "Shared with you, read-only" : checkoutText(row)}</span>
            {row.project.description && (
              <>
                <span aria-hidden>·</span>
                <span className="truncate">{row.project.description}</span>
              </>
            )}
          </p>
        </div>
        {!row.shared && (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button variant="secondary" size="sm" href={projectChatsPath(row.key)} leftIcon={<MessageSquare className="h-4 w-4" />}>
              Chats
            </Button>
            <Button variant="primary" size="sm" onClick={newChat} leftIcon={<SquarePen className="h-4 w-4" />} data-testid="project-new-chat">
              New chat
            </Button>
          </div>
        )}
      </div>
      {!row.shared && (
        <nav aria-label="Project" className="flex gap-1 border-b border-border">
          {TABS.map((t) => (
            <Link
              key={t.id}
              to={projectPath(row.key, t.id === "overview" ? undefined : t.id)}
              aria-current={tab === t.id ? "page" : undefined}
              data-testid={`project-tab-${t.id}`}
              className={cn(
                "no-color -mb-px inline-flex h-9 items-center border-b-2 px-3 text-ui transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                tab === t.id ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {t.label}
            </Link>
          ))}
        </nav>
      )}
    </div>
  );
}

// ── Overview ────────────────────────────────────────────────────────────────

function Panel({ title, count, action, children, testId }: { title: string; count?: number; action?: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <section className="flex flex-col overflow-hidden rounded-xl border border-border bg-card" data-testid={testId} aria-label={title}>
      <div className="flex min-h-11 items-center justify-between gap-2 border-b border-border px-4">
        <h2 className="flex items-baseline gap-2 text-ui font-medium text-foreground">
          {title}
          {count !== undefined && <span className="text-meta tabular-nums text-muted-foreground">{count}</span>}
        </h2>
        {action}
      </div>
      {children}
    </section>
  );
}

function PanelNote({ children }: { children: ReactNode }) {
  return <p className="px-4 py-4 text-ui text-muted-foreground">{children}</p>;
}

function ProjectOverview({ row, hostedAppsState }: { row: ProjectCatalogRow; hostedAppsState: "ok" | "no_access" | "unavailable" }) {
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]">
      <div className="flex min-w-0 flex-col gap-4">
        <AppsPanel row={row} hostedAppsState={hostedAppsState} />
        <CodePanel row={row} />
      </div>
      {!row.shared && (
        <div className="min-w-0 lg:self-start">
          <ChatsPanel row={row} />
        </div>
      )}
    </div>
  );
}

function AppsPanel({ row, hostedAppsState }: { row: ProjectCatalogRow; hostedAppsState: "ok" | "no_access" | "unavailable" }) {
  return (
    <Panel title="Apps" count={row.apps.length} testId="project-apps">
      {hostedAppsState === "no_access" && (
        <PanelNote>You can’t see this organization’s apps, so their deploy state is hidden. Ask an org owner for deploy access.</PanelNote>
      )}
      {hostedAppsState === "unavailable" && <PanelNote>App status is unavailable right now: the control plane didn’t answer.</PanelNote>}
      {row.apps.length === 0 ? (
        <PanelNote>
          No app in this project’s code yet. A repository with a <code className="font-mono text-code">forge.yaml</code> shows its app here once a machine has scanned it.
        </PanelNote>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {row.apps.map((app) => (
            <AppRow key={`${app.repoId ?? ""}/${app.subpath ?? ""}/${app.name}`} app={app} repoName={row.repos.find((r) => r.id === app.repoId)?.name} />
          ))}
        </ul>
      )}
    </Panel>
  );
}

function AppRow({ app, repoName }: { app: AppChipModel; repoName?: string }) {
  const deployed = app.hosted?.lastDeployedAt;
  const where = [repoName, app.subpath ? `${app.subpath}/` : ""].filter(Boolean).join(" · ");
  return (
    <li className="flex flex-col gap-1.5 px-4 py-3" data-testid="project-app" data-app={app.name}>
      <div className="flex min-w-0 items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <AppChip app={app} />
          {app.isStaticSite && (
            <span className="inline-flex items-center gap-1 text-meta text-muted-foreground">
              <Globe className="h-3 w-3" aria-hidden="true" />
              Static site
            </span>
          )}
        </div>
        {app.hosted && (
          <Link to={appPath(app.name)} className="shrink-0 text-meta font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            Open console
          </Link>
        )}
      </div>
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-meta">
        <dt className="text-muted-foreground">Environments</dt>
        <dd className="truncate text-foreground">{app.environments.length > 0 ? app.environments.join(", ") : "None live"}</dd>
        <dt className="text-muted-foreground">Last deploy</dt>
        <dd className="text-foreground" title={deployed ? formatAbsoluteTime(deployed) : undefined}>
          {deployed ? formatRelativeTime(deployed) : "Never"}
        </dd>
        {where && (
          <>
            <dt className="text-muted-foreground">Code</dt>
            <dd className="truncate text-foreground">{where}</dd>
          </>
        )}
        {linkLabel(app.link) && (
          <>
            <dt className="text-muted-foreground">Link</dt>
            <dd className="text-foreground">{linkLabel(app.link)}</dd>
          </>
        )}
      </dl>
      {app.hosted?.warning && (
        <p className="flex items-start gap-1.5 text-meta text-foreground">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" aria-hidden="true" />
          {app.hosted.warning}
        </p>
      )}
    </li>
  );
}

const INSTALL_STATE: Record<ProjectInstallState, { label: string; dot: string }> = {
  [ProjectInstallState.INSTALLED]: { label: "Checked out", dot: "bg-success" },
  [ProjectInstallState.INSTALLING]: { label: "Cloning…", dot: "bg-info" },
  [ProjectInstallState.FAILED]: { label: "Clone failed", dot: "bg-destructive" },
  [ProjectInstallState.UNSPECIFIED]: { label: "Unknown", dot: "bg-muted-foreground/40" },
};

function CheckoutRow({ checkout }: { checkout: CatalogCheckout }) {
  const state = INSTALL_STATE[checkout.installState] ?? INSTALL_STATE[ProjectInstallState.UNSPECIFIED];
  return (
    <li className="flex items-center justify-between gap-3 px-4 py-2" data-testid="project-checkout">
      <span className="flex min-w-0 items-center gap-2 text-ui text-foreground">
        <HardDrive className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{checkout.daemonName || "A machine"}</span>
      </span>
      <span className="flex shrink-0 items-center gap-1.5 text-meta text-muted-foreground">
        <span aria-hidden className={cn("h-2 w-2 rounded-full", state.dot)} />
        {state.label}
      </span>
    </li>
  );
}

function CodePanel({ row }: { row: ProjectCatalogRow }) {
  return (
    <Panel title="Code" testId="project-code">
      {row.repos.length === 0 ? (
        <PanelNote>{row.shared ? "Its repositories are its owner’s." : row.project.path || "No repository found yet."}</PanelNote>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {row.repos.map((repo) => {
            const apps = row.apps.filter((a) => a.repoId === repo.id);
            return (
              <li key={repo.id} className="flex flex-col gap-1.5 px-4 py-3" data-testid="project-repo">
                <div className="flex min-w-0 items-center gap-2">
                  <FolderGit2 className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="truncate text-ui font-medium text-foreground">{repo.name}</span>
                  {repo.relativePath && <span className="truncate font-mono text-code text-muted-foreground">{repo.relativePath}/</span>}
                </div>
                <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 pl-6 text-meta text-muted-foreground">
                  {repo.remote ? (
                    <a
                      href={`https://${repo.remote}`}
                      target="_blank"
                      rel="noreferrer"
                      className="inline-flex min-w-0 items-center gap-1 text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <span className="truncate">{repo.remote}</span>
                      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
                    </a>
                  ) : (
                    <span>{row.shared ? "Remote not shared" : "No remote"}</span>
                  )}
                  {apps.length > 0 && (
                    <span className="flex flex-wrap items-center gap-1">
                      {apps.map((app) => (
                        <AppChip key={`${app.subpath ?? ""}/${app.name}`} app={app} />
                      ))}
                    </span>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {!row.shared && (
        <div className="border-t border-border">
          <div className="flex min-h-10 items-center justify-between px-4">
            <h3 className="text-meta font-medium text-muted-foreground">Machines</h3>
            <Link to={projectPath(row.key, "settings")} className="text-meta font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              Manage
            </Link>
          </div>
          {row.checkouts.length === 0 ? (
            <PanelNote>Not checked out on any machine yet.</PanelNote>
          ) : (
            <ul className="pb-2">
              {row.checkouts.map((c) => (
                <CheckoutRow key={c.daemonId} checkout={c} />
              ))}
            </ul>
          )}
        </div>
      )}
    </Panel>
  );
}

const RECENT_CHATS = 8;

function ChatsPanel({ row }: { row: ProjectCatalogRow }) {
  const navigate = useNavigate();
  const { data: chats = [], isLoading, isError } = useChatList(row.key);
  const newChat = useNewChatInProject(row.key);
  const recent = useMemo(
    () =>
      chats
        .filter((chat) => chat.projectId === row.key)
        .sort((a, b) => (Date.parse(b.lastActive) || 0) - (Date.parse(a.lastActive) || 0))
        .slice(0, RECENT_CHATS),
    [chats, row.key],
  );
  const open = (chat: Chat) => {
    void openChat(chat)
      .then(() => navigate({ to: projectChatsPath(row.key) }))
      .catch((err: unknown) => {
        logger.error("[ProjectPage] Failed to open chat", err);
        toast.error("Could not open that chat");
      });
  };
  const total = chats.filter((chat) => chat.projectId === row.key).length;
  return (
    <Panel
      title="Chats"
      count={total || undefined}
      testId="project-chats"
      action={
        <button
          type="button"
          onClick={newChat}
          className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 text-meta font-medium text-primary-ink hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <SquarePen className="h-3.5 w-3.5" aria-hidden="true" />
          New chat in this project
        </button>
      }
    >
      {isLoading ? (
        <SkeletonLoader variant="text" count={3} className="m-4" />
      ) : isError ? (
        <PanelNote>Couldn’t load the chats.</PanelNote>
      ) : recent.length === 0 ? (
        <PanelNote>No chats here yet. Start one and it works in this project’s code.</PanelNote>
      ) : (
        <ul className="py-1">
          {recent.map((chat) => (
            <li key={chat.id}>
              <button
                type="button"
                onClick={() => open(chat)}
                className="flex w-full min-w-0 items-center justify-between gap-3 px-4 py-2 text-left hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                data-testid="project-chat"
              >
                <span className="truncate text-ui text-foreground">{chat.title || "Untitled chat"}</span>
                <span className="shrink-0 text-meta text-muted-foreground">{formatRelativeTime(chat.lastActive)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

// ── Workspaces ──────────────────────────────────────────────────────────────

/**
 * Settings → Workspaces' content for this project. Its panels follow the
 * app's one current project (SettingsProjectScope's rule), so the tab makes
 * this project current first, as picking it in the sidebar would.
 */
function ProjectWorkspaces({ project }: { project: Project }) {
  const navigate = useNavigate();
  const currentId = useProjectStore((state) => state.currentProject?.id);
  const selectProject = useProjectStore((state) => state.selectProject);
  useEffect(() => {
    if (currentId !== project.id) void selectProject(project);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per project; the store's identity is stable
  }, [project.id, currentId]);
  if (currentId !== project.id) {
    return (
      <div className="rounded-xl border border-border bg-card" aria-busy="true">
        <SkeletonLoader variant="list-item" count={3} />
      </div>
    );
  }
  return <WorkspacesBody onOpened={() => void navigate({ to: projectChatsPath(project.id) })} />;
}

// ── Settings ────────────────────────────────────────────────────────────────

/** Settings → Projects' controls for this one project, the same components. */
function ProjectSettings({ project }: { project: Project }) {
  const navigate = useNavigate();
  const updateProject = useProjectStore((state) => state.updateProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);
  const { prefs, setHidden, writable } = useSidebarProjectPrefs();
  const { canSwitch: canSwitchOrg } = useActiveOrg();
  const [removing, setRemoving] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [moving, setMoving] = useState(false);

  const rename = async (name: string) => {
    try {
      await updateProject(project.id, { name });
    } catch (err) {
      logger.error("[ProjectPage] Failed to rename project", err);
      toast.error(`Could not rename ${project.name}`);
    }
  };
  const remove = async () => {
    setRemoving(true);
    try {
      await deleteProject(project.id);
      setConfirmRemove(false);
      void navigate({ to: projectsPath() });
    } catch (err) {
      logger.error("[ProjectPage] Failed to remove project", err);
      toast.error(`Could not remove ${project.name}`);
    } finally {
      setRemoving(false);
    }
  };

  return (
    <div className="flex flex-col gap-6" data-testid="project-settings">
      <ProjectsTable
        projects={[project]}
        onOpen={() => void navigate({ to: projectChatsPath(project.id) })}
        onRemove={() => setConfirmRemove(true)}
        onRename={(_, name) => void rename(name)}
        hiddenProjectIds={prefs.hidden}
        onSetHidden={writable ? setHidden : undefined}
      />
      <ProjectMachines project={project} />
      {canSwitchOrg && <ProjectMoveSection project={project} onMove={() => setMoving(true)} />}
      {moving && <MoveProjectOrgDialog project={project} isOpen={moving} onClose={() => setMoving(false)} />}
      <RemoveProjectModal
        isOpen={confirmRemove}
        project={confirmRemove ? project : null}
        isRemoving={removing}
        onClose={() => {
          if (!removing) setConfirmRemove(false);
        }}
        onConfirm={() => void remove()}
      />
    </div>
  );
}
