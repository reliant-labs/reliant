// Copyright (c) 2025 Reliant Labs

/**
 * The app console's CODE card (control-plane
 * docs/design/unified-shell-and-projects.md §8.4): where the app is deployed
 * from, and the person's projects that hold its code.
 *
 *   Code          the recorded source — remote@subpath (or "deployed from a
 *                 local checkout"), the last deployed commit and when; every
 *                 source when there are several, under the two-repositories
 *                 warning (D5).
 *   In your       the person's projects that contain the app, "deployed from
 *   projects      here" (SOURCE) before "same name" (NAME), each with Open
 *                 and Start a chat here (a new chat in that project, tagged
 *                 with this app: D4).
 *   Shared with   teammates' projects shared with the org that contain it
 *   the org       (Open only: a viewer starts no chat in them).
 *   Get the code  when none of the person's projects has it: the Add project
 *                 dialog's clone form, filled from the recorded remote.
 *
 * All of it comes from the project catalog (appCodeModel.ts). "No access" and
 * "unavailable" are the catalog's own states: the projects still list (they
 * are reliant's), but where the app was deployed from is not known. In the
 * forge shell there are no Reliant projects, so the card is not shown.
 */

import { useState } from "react";
import { Download, MessageSquarePlus } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useProjectCatalog } from "@/hooks/project-catalog-queries";
import { projectPath } from "@/lib/paths";
import { isForgeShell } from "@/lib/shell";
import { toast } from "@/lib/toast-manager";
import { openAddProject } from "@/store/addProjectDialogStore";
import { openNewChat } from "@/store/openChat";

import { BUTTON_SMALL } from "../Observability/Dashboards/parts";
import { Ago, INK_BUTTON, INK_LINK, Note, Panel } from "../Releases/releaseParts";
import { appCode, cloneUrlOf, projectLinkLabel, shortCommit, sourceLabel, type AppCode, type AppCodeProject } from "./appCodeModel";

/**
 * Start a new chat in `projectId` about `appName`: the new-chat view
 * (`/new?project=&app=`, openNewChat), its composer showing the app's tag.
 */
export function useStartAppChat(appName: string) {
  const [starting, setStarting] = useState<string | null>(null);
  const start = async (projectId: string) => {
    setStarting(projectId);
    try {
      await openNewChat({ projectId, appName });
    } catch (error) {
      void toast.error(error);
    } finally {
      setStarting(null);
    }
  };
  return { start, starting };
}

export function AppCodeCard({ app }: { app: string }) {
  if (isForgeShell()) return null;
  return <AppCodeCardBody app={app} />;
}

function AppCodeCardBody({ app }: { app: string }) {
  const { catalog, isLoading, error, refetch } = useProjectCatalog();
  const { start, starting } = useStartAppChat(app);

  let body;
  if (catalog) {
    body = <AppCodeView app={app} code={appCode(catalog, app)} onStartChat={(id) => void start(id)} starting={starting} />;
  } else if (isLoading) {
    body = <div className="mx-4 mb-3 h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading where the code is" />;
  } else {
    body = (
      <p className="px-4 pb-3 text-xs text-ink-muted" data-testid="app-code-error">
        {error ? "Where this app's code is could not be read right now. " : "Where this app's code is is not known here. "}
        {error ? (
          <button type="button" className={INK_BUTTON} onClick={refetch}>
            Retry
          </button>
        ) : null}
      </p>
    );
  }
  return (
    <Panel title="Code" flush testId="app-code-card">
      {body}
    </Panel>
  );
}

export interface AppCodeViewProps {
  app: string;
  code: AppCode;
  onStartChat: (projectId: string) => void;
  /** The project a chat is being started in, while it is. */
  starting?: string | null;
}

/** The card's body for one answer: what it says, with no reads of its own. */
export function AppCodeView({ app, code, onStartChat, starting = null }: AppCodeViewProps) {
  const getTheCode = () => {
    const repoUrl = cloneUrlOf(code.sources);
    openAddProject(repoUrl ? "clone" : undefined, repoUrl ? { repoUrl } : undefined);
  };
  return (
    <div className="space-y-3 px-4 pb-3 text-xs" data-testid="app-code" data-state={code.state}>
      <Source code={code} />
      {code.warning && (
        <Note tone="warning" testId="app-code-warning">
          {code.warning}
        </Note>
      )}

      <section aria-labelledby="app-code-yours" className="space-y-1">
        <h3 id="app-code-yours" className="font-medium text-ink">
          In your projects
        </h3>
        {code.yours.length > 0 ? (
          <ProjectList projects={code.yours} onStartChat={onStartChat} starting={starting} testId="app-code-yours" />
        ) : (
          <div className="flex flex-wrap items-center justify-between gap-2" data-testid="app-code-none">
            <span className="text-ink-muted">None of your projects has {app}'s code.</span>
            <button type="button" className={BUTTON_SMALL} onClick={getTheCode} data-testid="app-code-get">
              <Download className="h-3 w-3" aria-hidden="true" />
              Get the code
            </button>
          </div>
        )}
      </section>

      {code.shared.length > 0 && (
        <section aria-labelledby="app-code-shared" className="space-y-1">
          <h3 id="app-code-shared" className="font-medium text-ink">
            Shared with the org
          </h3>
          <ProjectList projects={code.shared} testId="app-code-shared" />
        </section>
      )}
    </div>
  );
}

function Source({ code }: { code: AppCode }) {
  if (code.state === "no_access") {
    return (
      <p className="text-ink-muted" data-testid="app-code-source-state">
        You can't read this org's apps, so where this app is deployed from is not shown.
      </p>
    );
  }
  if (code.state === "unavailable") {
    return (
      <p className="text-ink-muted" data-testid="app-code-source-state">
        Where this app is deployed from is unavailable right now.
      </p>
    );
  }
  if (code.sources.length === 0) {
    return (
      <p className="text-ink-muted" data-testid="app-code-source-none">
        No deploy has recorded where its code came from yet. The next reliant forge env deploy records it.
      </p>
    );
  }
  return (
    <ul className="space-y-1.5" data-testid="app-code-sources">
      {code.sources.map((source) => (
        <li key={`${source.remote}@${source.subpath}`} className="min-w-0">
          <div className="flex min-w-0 items-baseline gap-1.5">
            <span className="shrink-0 text-ink-muted">Code</span>
            {source.remote ? (
              <span className="min-w-0 truncate font-mono text-ink" title={sourceLabel(source)}>
                {sourceLabel(source)}
              </span>
            ) : (
              <span className="min-w-0 truncate text-ink">deployed from {sourceLabel(source)}</span>
            )}
          </div>
          {source.commit && (
            <div className="text-ink-muted">
              last deployed commit <span className="font-mono text-ink">{shortCommit(source.commit)}</span> <Ago at={source.lastSeenAt} />
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

function ProjectList({
  projects,
  onStartChat,
  starting = null,
  testId,
}: {
  projects: AppCodeProject[];
  /** Absent for projects the person may only view. */
  onStartChat?: (projectId: string) => void;
  starting?: string | null;
  testId: string;
}) {
  return (
    <ul className="divide-y divide-border/70" data-testid={testId}>
      {projects.map(({ row, link, subpath }) => (
        <li key={row.key} className="min-w-0 space-y-1 py-2 first:pt-1" data-testid={`app-code-project-${row.key}`} data-link={link ?? "unknown"}>
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="min-w-0 truncate text-sm font-medium text-ink" title={row.project.path || row.name}>
              {row.name}
            </span>
            <span className="min-w-0 shrink-[2] truncate text-ink-muted">
              {projectLinkLabel(link)}
              {subpath ? ` · ${subpath}/` : ""}
            </span>
          </div>
          <div className="flex items-center gap-3">
            <Link href={projectPath(row.project.id)} className={INK_LINK} aria-label={`Open ${row.name}`}>
              Open
            </Link>
            {onStartChat && (
              <button
                type="button"
                className={BUTTON_SMALL}
                onClick={() => onStartChat(row.project.id)}
                disabled={starting !== null}
                aria-label={`Start a chat about this app in ${row.name}`}
                data-testid={`app-code-start-${row.key}`}
              >
                <MessageSquarePlus className="h-3 w-3" aria-hidden="true" />
                {starting === row.project.id ? "Starting…" : "Start a chat here"}
              </button>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}
