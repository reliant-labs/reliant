// Copyright (c) 2026 Reliant Labs

/**
 * The current project's app in the project shell's nav (U6b, prototype): its
 * console sections as top-level rows — Overview, Issues, Performance,
 * Sessions, … Configure — straight from the console's grouped nav
 * (Forge/Console/groupedNav.tsx), so the nav reads like the forge console's.
 * While the org-wide Observability views are open, they nest
 * under an Observability row here too.
 */

import { useMemo } from "react";
import { Activity, Gauge } from "lucide-react";

import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";

import { buildGroupedConsoleNav, type GroupedNavInput } from "../../Forge/Console/groupedNav";
import { consolePageOf, orgPageOf } from "../../Forge/Console/paths";
import { useConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import { buildOrgNav } from "../../Forge/Org/OrgShell";
import { SidebarNavTree } from "./NavTree";
import { SidebarRow } from "./primitives";

export interface AppsNavSectionProps {
  pathname: string;
  search: Record<string, unknown>;
  onNavigate: (href: string) => void;
}

/**
 * The current project's app sections. Every project has them (owner,
 * 2026-11-20): on an app with nothing deployed, each page says what it will
 * show and how to deploy (Forge/Console/AppSectionEmptyState.tsx).
 */
export function ProjectAppNav({ app, pathname, search, onNavigate }: { app: string | null } & AppsNavSectionProps) {
  const orgPage = orgPageOf(pathname);
  return (
    <>
      {app && <AppSections app={app} pathname={pathname} search={search} onNavigate={onNavigate} />}
      {orgPage && <OrgObservabilityRows pathname={pathname} search={search} onNavigate={onNavigate} />}
    </>
  );
}

/**
 * The app's rows for the page on screen. Off the console (a chat, the Inbox)
 * the model's page defaults to the Overview, but no app row is the current
 * one there: they are all ways out.
 */
export function appNavFor(model: GroupedNavInput, pathname: string) {
  return buildGroupedConsoleNav({ ...model, hostPageActive: model.hostPageActive || !consolePageOf(pathname) });
}

function AppSections({ app, pathname, search, onNavigate }: { app: string } & AppsNavSectionProps) {
  const model = useConsoleNavModel(app, pathname, search);
  const nav = appNavFor(model, pathname);
  return (
    <section
      aria-label={`${app} app`}
      // The forge journey's "A Forge app's console" step spotlights the app's sections.
      data-tour="forge-console-nav"
      data-testid="sidebar-app-sections"
      data-app={app}
      className="space-y-0.5 px-2"
    >
      <SidebarNavTree items={nav.items} depth={0} onNavigate={onNavigate} testIdPrefix="sidebar-app-section" />
    </section>
  );
}

/** The app's Activity row (the grouped nav's last Configure row), for the nav's footer. */
export function appActivityOf(model: GroupedNavInput, pathname: string) {
  const configure = appNavFor(model, pathname).items.find((item) => item.label === "Configure");
  return configure?.children?.find((item) => item.label === "Activity");
}

/** The app's Activity, for the nav's footer (as in the forge console). */
export function AppActivityRow({ app, pathname, search, onNavigate }: { app: string } & AppsNavSectionProps) {
  const model = useConsoleNavModel(app, pathname, search);
  const activity = appActivityOf(model, pathname);
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
