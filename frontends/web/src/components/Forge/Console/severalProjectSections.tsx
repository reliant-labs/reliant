// Copyright (c) 2026 Reliant Labs

/**
 * A console page ONCE PER PROJECT (owner, 2026-11-25: "if multiple projects
 * are selected … some of the overview features don't work and instead show a
 * project picker to select 1 project"). While the shell shows several
 * projects, a list page that does not aggregate itself (severalProjects.tsx
 * severalModeOf: "sections") renders one section per project, under one
 * title and the shared project filter (`?only=`).
 *
 * Every console page reads its project from the console context
 * (consoleContext.ts), so a section is the page itself, mounted inside a
 * context of that project's own: its Reliant project, roster, environment
 * scope and hrefs (ProjectConsoleScope). ConsoleLayout hands the route's
 * Outlet in as `children`; no page needed to change.
 *
 * Fan-out: a section's body mounts only when it comes near the viewport
 * (LazyMount), and stays mounted after. "All projects" with many projects
 * therefore reads only the sections on screen; `?only=` narrows to one.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { AppScopeProvider } from "@/fixtures/scope";
import { useForgeRoster } from "@/hooks/forge-queries";

import { ProjectMark } from "../../Layout/ProjectMark";
import { navigateWithShellScope } from "../../Layout/shellScopeNav";
import { useShellLastPick } from "../../Layout/sidebar/shellProject";
import { rosterReadFailed, useForgeConsole } from "./consoleContext";
import { ForgeConsoleContext, type ForgeConsole } from "./consoleContextValue";
import { consoleCrumbs, PAGE_SUBTITLES } from "./consoleCrumbs";
import { useConsoleReliantProject } from "./consoleHost";
import { rememberedEnv, resolveEnvScope } from "./envScope";
import { consolePath, isTestsPage, withSearch, type ConsolePage } from "./paths";
import { ProjectFilter, useOnlyFilter, type SeveralApp } from "./severalProjects";
import { InProjectSection } from "./severalSectionContext";

/**
 * `children` inside the console context of `project`'s app, as ConsoleLayout
 * builds it for the app's own console, but never selecting the project (the
 * page shows several) and pinned to the catalog's project (`project.key`), so
 * a second checkout of the same app never asks which.
 */
export function ProjectConsoleScope({ project, page, children }: { project: SeveralApp; page: ConsolePage; children: ReactNode }) {
  const app = project.app;
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { env?: unknown };
  const { listed, projectId } = useConsoleReliantProject(app, { select: false, pinned: project.key.startsWith("app:") ? null : project.key });
  const roster = useForgeRoster(listed ? projectId : null, app);
  const settled = listed && !roster.isLoading && !roster.topology.isLoading;
  const known = useMemo(() => (settled ? roster.envs.map((env) => env.name) : null), [settled, roster.envs]);
  const env = resolveEnvScope(typeof search.env === "string" ? search.env : undefined, rememberedEnv(app), known);
  // One scope for the page: an environment picked in one section is the URL's, for all of them.
  const setEnv = useCallback(
    (next: string) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, env: next }), replace: true } as never),
    [navigate],
  );
  const href = useCallback(
    (target: ConsolePage, extra: Record<string, unknown> = {}) => withSearch(consolePath(app, target), { env, ...extra }),
    [app, env],
  );
  const value = useMemo<ForgeConsole>(() => ({ forgeProject: app, projectId, page, env, setEnv, roster, href }), [app, projectId, page, env, setEnv, roster, href]);

  if (!listed || (roster.isLoading && roster.envs.length === 0)) return <SectionLoading name={project.name} />;
  // Nothing deployed: a line, not the page's getting-started guide once per project. Tests need no deploy.
  if (settled && roster.envs.length === 0 && !rosterReadFailed(roster) && !isTestsPage(page)) {
    return (
      <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted" data-testid={`several-section-undeployed-${app}`}>
        {project.name} has nothing deployed yet.
      </p>
    );
  }
  return (
    <AppScopeProvider app={app}>
      <ForgeConsoleContext.Provider value={value}>{children}</ForgeConsoleContext.Provider>
    </AppScopeProvider>
  );
}

function SectionLoading({ name }: { name: string }) {
  return (
    <div aria-busy="true" aria-label={`Loading ${name}`} className="space-y-2">
      <SkeletonLoader variant="table-row" count={3} />
    </div>
  );
}

/**
 * Mounts `children` once the placeholder comes within a screen of the
 * viewport, and keeps them after. Without IntersectionObserver (tests, old
 * engines) it mounts at once.
 */
export function LazyMount({ children, placeholder }: { children: ReactNode; placeholder: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [shown, setShown] = useState(() => typeof IntersectionObserver === "undefined");
  useEffect(() => {
    if (shown || !ref.current) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setShown(true);
      },
      { rootMargin: "800px 0px" },
    );
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, [shown]);
  return shown ? <>{children}</> : <div ref={ref}>{placeholder}</div>;
}

/** The page's name, as the frame's breadcrumb ends. */
function pageTitle(page: ConsolePage, env: string): string {
  const crumbs = consoleCrumbs("", page, env);
  return crumbs[crumbs.length - 1]?.label ?? "Overview";
}

/** The key PAGE_SUBTITLES files the page under. */
function subtitleKey(page: ConsolePage): string {
  return page.kind === "obs-view" ? page.view : page.kind;
}

/** The page on screen once per project: one title, the project filter, then a section each. */
export function SeveralProjectSections({ apps, children }: { apps: readonly SeveralApp[]; children: ReactNode }) {
  const { page, env } = useForgeConsole();
  const { only, shown, setOnly } = useOnlyFilter(apps);
  const title = pageTitle(page, env);
  const subtitle = PAGE_SUBTITLES[subtitleKey(page)];
  return (
    <div className="min-w-0 space-y-6" data-testid="several-project-sections" data-page={page.kind}>
      <div>
        <h1 className="text-title text-ink">{title}</h1>
        <p className="mt-1 text-ui text-ink-muted">
          {subtitle ? `${subtitle}. ` : ""}
          {only ? "One project" : `${apps.length} projects`}, each below.
        </p>
      </div>
      <ProjectFilter apps={apps} only={only} onChange={setOnly} />
      {shown.map((project) => (
        <ProjectSection key={project.key} project={project} page={page}>
          {children}
        </ProjectSection>
      ))}
    </div>
  );
}

function ProjectSection({ project, page, children }: { project: SeveralApp; page: ConsolePage; children: ReactNode }) {
  const setLastPick = useShellLastPick((state) => state.set);
  const alone = consolePath(project.app, page);
  return (
    <section className="space-y-3" aria-labelledby={`several-section-${project.key}`} data-testid={`several-section-${project.app}`}>
      <div className="flex items-center justify-between gap-3 border-b border-border-subtle pb-2">
        <h2 id={`several-section-${project.key}`} className="flex min-w-0 items-center gap-2 text-section text-ink">
          <ProjectMark name={project.name} />
          <span className="min-w-0 truncate">{project.name}</span>
          {project.app !== project.name && <span className="min-w-0 truncate text-meta font-normal text-ink-muted">{project.app}</span>}
        </h2>
        <a
          href={alone}
          onClick={(event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
            event.preventDefault();
            // Into this project alone: the shell's one project, on the same page of its app.
            setLastPick(project.key);
            navigateWithShellScope(undefined, { to: alone });
          }}
          className="no-color inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-ui text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid={`several-section-open-${project.app}`}
        >
          Open {project.name} alone
          <ChevronRight className="size-3.5" aria-hidden="true" />
        </a>
      </div>
      <LazyMount placeholder={<SectionLoading name={project.name} />}>
        <InProjectSection.Provider value={project}>
          <ProjectConsoleScope project={project} page={page}>
            {children}
          </ProjectConsoleScope>
        </InProjectSection.Provider>
      </LazyMount>
    </section>
  );
}
