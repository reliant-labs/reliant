// Copyright (c) 2026 Reliant Labs

/**
 * The console's frame INSIDE THE ONE SHELL (U6): no nav column — the app's
 * sections are rows of the shell's left nav (Layout/sidebar/navConfig/
 * items.tsx) — just a slim bar with the breadcrumb and the ONE environment
 * selector, over the page, with the page's Learn link (consoleLearn.ts)
 * beside the selector.
 *
 * `forge-ui` is REQUIRED, not cosmetic: inside it forge's `accent` token
 * resolves to reliant's primary action colour; outside it, to reliant's muted
 * hover tint (src/index.css). `forge-console` raises the control edge to 3:1
 * so filters and toggles read as controls beside the charts.
 *
 * It is the console's only frame: the app, the public demo
 * (demoShellRoutes.tsx) and the dev previews (Forge/__preview__/
 * PreviewShellFrame.tsx) all draw the console inside the shell.
 */

import type { ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import { PageMain, ShellPageHeader } from "@/components/Layout/shell/shellFrame";

import { LearnLink } from "@/lib/learnLinks";

import { appDisplayName } from "../Sample/sampleApp";
import { consoleCrumbs, type Crumb } from "./consoleCrumbs";
import { learnSectionOf } from "./consoleLearn";
import { useConsoleNavExtras } from "./consoleNavExtras";
import { EnvScopeControl } from "./EnvScopeControl";
import type { ScopeOption } from "./envScope";
import { consolePath, pinnedEnvOf, withSearch, type ConsolePage } from "./paths";
import { severalModeOf, useSeveralApps } from "./severalProjects";
import { useRouteUsesEnvScope } from "./useConsoleNavModel";

export interface ConsoleFrameProps {
  project: string;
  page: ConsolePage;
  env: string;
  dashboardTitle?: string;
  scope: { long: ScopeOption[]; previews: ScopeOption[] };
  platformWide?: boolean;
  onEnvChange: (env: string) => void;
  children: ReactNode;
}

export function ConsoleFrame({ project, page, env, dashboardTitle, scope, platformWide, onEnvChange, children }: ConsoleFrameProps) {
  const { crumbs: crumbsOverride, envOrder } = useConsoleNavExtras();
  const rank = (name: string) => (envOrder?.includes(name) ? envOrder.indexOf(name) : (envOrder?.length ?? 0));
  const long = envOrder ? [...scope.long].sort((a, b) => rank(a.name) - rank(b.name)) : scope.long;
  const own = consoleCrumbs(project, page, env, dashboardTitle);
  const crumbs = crumbsOverride?.(page, own) ?? own;
  // Every console page names its guide in the bar (U6d), so no page has to.
  const learn = learnSectionOf(page);
  // A page that opted out of the env scope (useConsoleNavModel.ts) gets no selector.
  const envScoped = useRouteUsesEnvScope();
  // Several projects in the shell: a list page is about all of them (severalProjects.tsx), so the
  // trail starts there, not at the focused app; a page about one object still names its app.
  const several = useSeveralApps();
  const across = several && severalModeOf(page) !== "one" ? (several.mode === "all" ? "All projects" : `${several.apps.length} projects`) : null;

  return (
    <div className="forge-ui forge-console flex h-full min-h-0 w-full flex-col" data-testid="forge-console-frame">
      <ShellPageHeader
        testId="forge-console-header"
        actions={
          <>
          {learn && <LearnLink section={learn} />}
          {envScoped && (
            <EnvScopeControl
              value={env}
              long={long}
              previews={scope.previews}
              onChange={onEnvChange}
              pinnedEnv={pinnedEnvOf(page)}
              platformWide={platformWide}
            />
          )}
          </>
        }
      >
        <ConsoleBreadcrumb project={project} env={env} crumbs={crumbs} across={across} />
      </ShellPageHeader>
      <PageMain className="relative min-h-0 flex-1 overflow-y-auto px-8 pb-12 pt-6 max-md:px-4 max-md:pt-4" data-testid="forge-console-page">
        {children}
      </PageMain>
    </div>
  );
}

/**
 * The console's breadcrumb: the app (its Overview), then the page's trail.
 * `across` names the projects a page across several shows in the app's place
 * (its Overview is theirs too: ProjectsOverview).
 */
export function ConsoleBreadcrumb({ project, env, crumbs, across }: { project: string; env: string; crumbs: Crumb[]; across?: string | null }) {
  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 text-sm text-ink-muted">
      <Link href={withSearch(consolePath(project, { kind: "overview" }), { env })} className="truncate hover:text-ink">
        {across ?? appDisplayName(project)}
      </Link>
      {crumbs.map((crumb, index) => (
        <span key={`${crumb.label}:${index}`} className="flex min-w-0 items-center gap-1.5 max-sm:hidden max-sm:last:inline-flex">
          <span aria-hidden="true" className="text-ink-subtle">
            ›
          </span>
          {crumb.href ? (
            <Link href={crumb.href} className="truncate hover:text-ink">
              {crumb.label}
            </Link>
          ) : (
            <span aria-current="page" className="truncate font-medium text-ink">
              {crumb.label}
            </span>
          )}
        </span>
      ))}
    </nav>
  );
}
