// Copyright (c) 2026 Reliant Labs

/**
 * The current project's app in the project shell's nav (U6b, prototype): its
 * console sections as top-level rows — Overview, Issues, Performance,
 * Sessions, … Configure — straight from the console's grouped nav
 * (Forge/Console/groupedNav.tsx), so the nav reads like the forge console's.
 * A project with no deployed app shows one quiet line with the way to deploy
 * it instead. While the org-wide Observability views are open, they nest
 * under an Observability row here too.
 */

import { useMemo } from "react";
import { Activity, Gauge, Rocket } from "lucide-react";

import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { projectChatsPath, withSearch } from "@/lib/paths";

import { START_REPO } from "../../Apps/appStart";
import { buildGroupedConsoleNav } from "../../Forge/Console/groupedNav";
import { orgPageOf } from "../../Forge/Console/paths";
import { useConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import { buildOrgNav } from "../../Forge/Org/OrgShell";
import { SidebarNavTree } from "./NavTree";
import { SidebarRow } from "./primitives";

export interface AppsNavSectionProps {
  pathname: string;
  search: Record<string, unknown>;
  onNavigate: (href: string) => void;
}

/** The current project's app sections, or (no deployed app) the way to deploy one. */
export function ProjectAppNav({
  app,
  projectId,
  pathname,
  search,
  onNavigate,
}: { app: string | null; projectId: string | null } & AppsNavSectionProps) {
  const orgPage = orgPageOf(pathname);
  return (
    <>
      {app ? (
        <AppSections app={app} pathname={pathname} search={search} onNavigate={onNavigate} />
      ) : (
        projectId && <NoAppRow projectId={projectId} onNavigate={onNavigate} />
      )}
      {orgPage && <OrgObservabilityRows pathname={pathname} search={search} onNavigate={onNavigate} />}
    </>
  );
}

function AppSections({ app, pathname, search, onNavigate }: { app: string } & AppsNavSectionProps) {
  const model = useConsoleNavModel(app, pathname, search);
  const nav = buildGroupedConsoleNav(model);
  return (
    <section
      aria-label={`${app} app`}
      // The forge journey's "A Forge app's console" step spotlights the app's sections.
      data-tour="forge-console-nav"
      data-testid="sidebar-app-sections"
      data-app={app}
      className="space-y-0.5 px-2"
    >
      <SidebarNavTree items={nav.items} onNavigate={onNavigate} testIdPrefix="sidebar-app-section" />
    </section>
  );
}

/** The app's Activity, for the nav's footer (as in the forge console). */
export function AppActivityRow({ app, pathname, search, onNavigate }: { app: string } & AppsNavSectionProps) {
  const model = useConsoleNavModel(app, pathname, search);
  const activity = buildGroupedConsoleNav(model).footer[0];
  if (!activity?.href) return null;
  const href = activity.href;
  return (
    <SidebarRow
      icon={<Activity className="size-4" />}
      label={activity.label}
      href={href}
      onClick={() => onNavigate(href)}
      current={!!activity.active}
      testId="sidebar-app-activity"
    />
  );
}

/** A project with no deployed app: one quiet row that starts a chat to deploy it. */
function NoAppRow({ projectId, onNavigate }: { projectId: string; onNavigate: (href: string) => void }) {
  const href = withSearch(projectChatsPath(projectId), { start: START_REPO });
  return (
    <div className="px-2" data-testid="sidebar-no-app">
      <div className="rounded-md border border-border px-3 py-2.5">
        <p className="text-ui text-foreground">No app deployed yet</p>
        <p className="mt-0.5 text-meta text-muted-foreground">Deploy this project to see its errors, traces, data and users here.</p>
        <a
          href={href}
          onClick={(event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
            event.preventDefault();
            onNavigate(href);
          }}
          data-testid="sidebar-no-app-deploy"
          className="no-color mt-2 inline-flex h-7 items-center gap-1.5 rounded-md px-2 -ml-2 text-ui font-medium text-primary-ink transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Rocket className="size-3.5" aria-hidden="true" />
          Deploy
        </a>
      </div>
    </div>
  );
}

/** The org-wide Observability views, while one of them is open. */
function OrgObservabilityRows({ pathname, search, onNavigate }: AppsNavSectionProps) {
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  const alertsFiring = useMemo(() => (alerts.data ?? []).filter((alert) => alert.state === "firing").length, [alerts.data]);
  const page = orgPageOf(pathname) ?? { kind: "observability" as const };
  const observability = buildOrgNav({ page, apps: [], alertsFiring, search }).items.find((item) => item.label === "Observability");
  if (!observability?.href) return null;
  const href = observability.href;
  return (
    <div data-testid="sidebar-org-observability" className="mt-4 px-2">
      <SidebarRow
        icon={<Gauge className="size-4" />}
        label="All apps' observability"
        href={href}
        onClick={() => onNavigate(href)}
        current={!!observability.active}
        expanded
      />
      <div role="group" aria-label="Observability views" className="space-y-0.5 pb-2 pt-0.5">
        <SidebarNavTree items={observability.children ?? []} onNavigate={onNavigate} testIdPrefix="sidebar-org-obs" />
      </div>
    </div>
  );
}
