// Copyright (c) 2026 Reliant Labs

/**
 * The nav's Apps section (U6, design §4.1 with the owner's 2026-11-19
 * amendment): every app the person can see, each with its live state and one
 * click from its console; the OPEN app (the one whose console is on screen)
 * expands in place into its console's sections — Overview, Issues,
 * Performance, … Configure — so an app's pages are rows of this nav, never a
 * second nav column. While the org-wide Observability views are open, they
 * nest under an Observability row here too.
 */

import { useMemo } from "react";
import { ExternalLink, Gauge } from "lucide-react";

import Status from "@/components/forge-ui/status";
import { useApps } from "@/hooks/apps-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { appsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { primaryLink, type AppSummary } from "@/services/forge/apps";

import { verdictBusy, verdictTone } from "../../Apps/appParts";
import { buildGroupedConsoleNav } from "../../Forge/Console/groupedNav";
import { consolePageOf, consolePath, orgPageOf } from "../../Forge/Console/paths";
import { useConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import { buildOrgNav } from "../../Forge/Org/OrgShell";
import { isAt } from "./navLocation";
import { SidebarNavTree } from "./NavTree";
import { SIDEBAR_ICON_BUTTON, SidebarHeading, SidebarRow } from "./primitives";

export interface AppsNavSectionProps {
  pathname: string;
  search: Record<string, unknown>;
  onNavigate: (href: string) => void;
}

/** The app's live state: the health of the environment its site opens from, else its first. */
function AppDot({ app }: { app: AppSummary | undefined }) {
  const link = app ? primaryLink(app) : undefined;
  const env = app && (app.envs.find((candidate) => link && candidate.links.includes(link)) ?? app.envs[0]);
  if (!env) return <Status tone="neutral" size="md" />;
  // Mid-rollout the dot is a spinner, so a deploy never reads as a fault.
  return <Status tone={verdictTone(env.verdict)} size="md" busy={verdictBusy(env.verdict)} />;
}

export function AppsNavSection({ pathname, search, onNavigate }: AppsNavSectionProps) {
  const { apps } = useApps();
  const openApp = consolePageOf(pathname)?.project ?? null;
  const orgPage = orgPageOf(pathname);
  // The open app has its rows even before the list answers (a deep link, a cold load).
  const names = useMemo(() => {
    const listed = apps.map((app) => app.name);
    return openApp && !listed.includes(openApp) ? [openApp, ...listed] : listed;
  }, [apps, openApp]);
  const allApps = appsPath();
  const onAllApps = isAt(pathname, allApps);

  return (
    <section aria-label="Apps" data-testid="sidebar-apps">
      <SidebarHeading>
        <a
          href={allApps}
          onClick={(event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
            event.preventDefault();
            onNavigate(allApps);
          }}
          aria-current={onAllApps ? "page" : undefined}
          data-testid="sidebar-forge-button"
          data-onboarding="apps-button"
          className={cn(
            "no-color -mx-1 rounded-md px-1 transition-colors hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            onAllApps ? "font-medium text-foreground" : "text-muted-foreground",
          )}
        >
          Apps
        </a>
      </SidebarHeading>
      <div className="space-y-0.5 px-2">
        {names.map((name) => {
          const app = apps.find((candidate) => candidate.name === name);
          const open = name === openApp;
          const link = app ? primaryLink(app) : undefined;
          return (
            <div key={name}>
              <SidebarRow
                icon={<AppDot app={app} />}
                label={name}
                href={consolePath(name, { kind: "overview" })}
                onClick={() => onNavigate(consolePath(name, { kind: "overview" }))}
                expanded={open}
                className="group/app"
                testId={`sidebar-app-${name}`}
                action={
                  link && (
                    <a
                      href={link.url}
                      target="_blank"
                      rel="noreferrer"
                      aria-label={`Open ${name} at ${link.host}`}
                      title={`Open ${link.host}`}
                      className={cn(SIDEBAR_ICON_BUTTON, "no-color hidden group-hover/app:inline-flex group-focus-within/app:inline-flex")}
                      data-testid={`sidebar-app-link-${name}`}
                    >
                      <ExternalLink className="size-3.5" aria-hidden="true" />
                    </a>
                  )
                }
              />
              {open && <OpenAppSections project={name} pathname={pathname} search={search} onNavigate={onNavigate} />}
            </div>
          );
        })}
        {orgPage && <OrgObservabilityRows pathname={pathname} search={search} onNavigate={onNavigate} />}
      </div>
    </section>
  );
}

/** The open app's console sections, nested under its row. */
function OpenAppSections({ project, pathname, search, onNavigate }: { project: string } & AppsNavSectionProps) {
  const model = useConsoleNavModel(project, pathname, search);
  const nav = buildGroupedConsoleNav(model);
  return (
    <div
      role="group"
      aria-label={`${project} sections`}
      // The forge journey's "A Forge app's console" step spotlights the app's sections.
      data-tour="forge-console-nav"
      data-testid="sidebar-app-sections"
      className="space-y-0.5 pb-2 pt-0.5"
    >
      <SidebarNavTree items={[...nav.items, ...nav.footer]} onNavigate={onNavigate} testIdPrefix="sidebar-app-section" />
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
    <div data-testid="sidebar-org-observability">
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
