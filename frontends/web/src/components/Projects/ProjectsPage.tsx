// Copyright (c) 2026 Reliant Labs

/**
 * /projects — the nav's Projects entry (U6): what the person has shipped
 * (their apps, and the sites among them) and the code they work in (their
 * Reliant projects, each opening its chats).
 *
 * A first cut on purpose. U7 replaces it with the merged list of design §8.3,
 * where a project and the app built from its code are one row; until then the
 * two lists sit one above the other, and the filter narrows to apps or sites.
 * Apps and Sites were pages of their own; their old URLs (/apps, /sites)
 * redirect to the filters (lib/paths/legacy.ts).
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { FolderGit2, Plus } from "lucide-react";

import PageHeader from "@/components/forge-ui/page_header";
import Tabs from "@/components/forge-ui/tabs";
import { useProjects } from "@/hooks/project-queries";
import { formatRelativeTime } from "@/lib/relativeTime";
import { projectChatsPath, projectsPath, withSearch } from "@/lib/paths";

import { AppsBody } from "../Apps/AppsPage";
import { ProjectsOverview } from "../Forge/Console/ProjectsOverview";
import { useStartBuildingApp } from "../Apps/startBuilding";
import { SitesList } from "../ForgeShell/ForgeSitesPage";
import { chatSetupNeeded } from "../OnboardingFlow/onboardingGate";
import { SetUpChatsCard } from "../OnboardingFlow/SetUpChatsCard";
import { useChatSetup } from "../OnboardingFlow/useChatSetup";
import { AreaShell } from "../Layout/AreaShell";
import { useShellSeveral } from "../Layout/sidebar/shellProject";

type ProjectsFilter = "all" | "apps" | "sites";

const FILTERS: { id: ProjectsFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "apps", label: "Apps" },
  { id: "sites", label: "Sites" },
];

export function ProjectsPage() {
  return (
    <AreaShell areaPath={projectsPath()} areaLabel="Projects" areaNoun="projects">
      <ProjectsBody />
    </AreaShell>
  );
}

export function ProjectsBody() {
  // The filter is the URL's (`?show=apps|sites`), so a refresh, Back and a shared link keep it.
  const { show, browse } = useSearch({ strict: false }) as { show?: unknown; browse?: unknown };
  // Several projects in the shell: this page is their Home, the summary rows alone (U6b).
  const { mode: several } = useShellSeveral();
  const filter: ProjectsFilter = show === "apps" || show === "sites" ? show : "all";
  const navigate = useNavigate();
  const setFilter = (next: ProjectsFilter) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, show: next === "all" ? undefined : next }),
      replace: true,
    } as never);
  // A person who cannot run a chat yet lands here (the daemonless home, U6):
  // the way to set chats up leads the page.
  const setup = useChatSetup();
  // The apps list's own action, in the page's header now that the page heads the list.
  const startBuilding = useStartBuildingApp();

  if (several && browse === undefined) {
    return (
      <div className="space-y-4" data-testid="projects-page" data-mode="home">
        <div className="forge-ui">
          <ProjectsOverview />
        </div>
        <div className="mx-auto max-w-[var(--page-max)] text-right">
          <a
            href={withSearch(projectsPath(), { browse: 1 })}
            onClick={(event) => {
              if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
              event.preventDefault();
              void navigate({ href: withSearch(projectsPath(), { browse: 1 }) });
            }}
            className="no-color text-meta text-muted-foreground hover:text-foreground hover:underline"
            data-testid="projects-browse-all"
          >
            Browse all projects
          </a>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="projects-page">
      <PageHeader
        title="Projects"
        meta={<span>Your apps and sites, and the code you work on.</span>}
        actions={
          filter === "sites"
            ? []
            : [{ label: "Build an app", onClick: startBuilding, variant: "primary", icon: <Plus className="h-4 w-4" aria-hidden="true" /> }]
        }
        className="mb-0"
      />
      {chatSetupNeeded(setup) && (
        <SetUpChatsCard setup={setup} variant="page" onNavigate={(href) => void navigate({ href })} />
      )}
      <Tabs tabs={FILTERS} activeTab={filter} onChange={(id) => setFilter(id as ProjectsFilter)} ariaLabel="Show" />
      {filter === "sites" ? <SitesList header={false} /> : <AppsBody header={false} />}
      {filter === "all" && <CodeProjects />}
    </div>
  );
}

/** The person's Reliant projects, each opening its chats. */
function CodeProjects() {
  const { projects, isLoading } = useProjects();
  const navigate = useNavigate();
  if (isLoading || projects.length === 0) return null;
  const sorted = [...projects].sort((a, b) => b.last_active.localeCompare(a.last_active));
  return (
    <section aria-labelledby="projects-code-heading" data-testid="projects-code">
      <h2 id="projects-code-heading" className="mb-2 text-section text-foreground">
        Code
      </h2>
      <ul className="divide-y divide-border-subtle rounded-xl border border-border">
        {sorted.map((project) => {
          const href = projectChatsPath(project.id);
          return (
            <li key={project.id}>
              <a
                href={href}
                onClick={(event) => {
                  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
                  event.preventDefault();
                  void navigate({ href });
                }}
                className="no-color flex h-[var(--table-row-h)] items-center gap-3 px-4 text-ui text-foreground transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                data-testid={`projects-code-${project.id}`}
              >
                <FolderGit2 className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="min-w-0 flex-1 truncate font-medium hover:underline">{project.name}</span>
                {project.last_active && (
                  <span className="shrink-0 text-meta text-muted-foreground">{formatRelativeTime(project.last_active)}</span>
                )}
              </a>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
