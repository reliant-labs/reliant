// Copyright (c) 2025 Reliant Labs

/**
 * The forge surface's CHROME — sidebar, nav, scope — with no data, no routing
 * and no project resolution.
 *
 * WHY THIS IS A SEPARATE COMPONENT, and it is not a refactor for tidiness.
 *
 * The forge screens are hard to look at while building them: every route is
 * behind auth, and the states that matter most visually (a destroyed secret
 * version, a cluster that could not be reached, a declared-but-never-set key)
 * are the ones hardest to produce on demand against a live backend. So each
 * screen grew a dev-only preview harness rendering it against fabricated data.
 * Those harnesses render THIS, the same component the real layout renders, so
 * a harness cannot drift from the product chrome — there is only one copy.
 *
 * ForgeLayout owns everything stateful — auth, the project-resolution ladder,
 * the Escape binding, the router Outlet. This owns only what you can see.
 *
 * ── THE NAV IS THE ENVIRONMENT ROSTER ───────────────────────────────────────
 *
 * Overview and Domains, then every environment filed by what it IS:
 *
 *   Local           runs on a developer machine via `forge env up`.
 *   Deployed        everything the control plane records as deployed.
 *   Not registered  declared in the checkout, unknown to Reliant. Only the
 *                   daemon can see these, which is why they are a separate,
 *                   labelled group rather than mixed into the record — and
 *                   why an asleep daemon empties only this group.
 *
 * While the backend list is still arriving the sections are SKELETONS, not
 * absent: a sidebar that renders two links and then grows four more reads as
 * a project with no environments for the first second of every visit.
 */

import { useMemo, type ReactNode } from "react";
import { CircleDashed, Cloud, Cpu, Globe, LayoutGrid } from "lucide-react";

import SidebarLayout from "@/components/forge-ui/sidebar_layout";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import type { EnvLifecycle, RosterSource } from "@/services/forge/roster";

/** The Overview's path. */
export const FORGE_OVERVIEW_PATH = "/forge";

/**
 * Custom domains. The second fixed destination, and the only screen here that
 * is not about one environment: a domain is org-scoped and its binding is
 * meant to move between environments, so it sits beside the Overview under
 * Project rather than under any one env.
 */
export const FORGE_DOMAINS_PATH = "/forge/domains";

/** The path of one environment's page. */
export function forgeEnvPath(env: string): string {
  return `/forge/env/${encodeURIComponent(env)}`;
}

export interface ForgeNavEnv {
  name: string;
  lifecycle: EnvLifecycle;
  source: RosterSource;
  /**
   * What this environment's QUEUED deploy waits on ("billing"), when one is.
   * Marked in the nav because it is the one state that needs a person and
   * would otherwise be visible only on the environment's own page.
   */
  queuedOn?: string;
}

/** Which nav section an environment is filed under. */
export function navSectionOf(env: Pick<ForgeNavEnv, "lifecycle" | "source">): string {
  if (env.source === "checkout") return "Not registered";
  return env.lifecycle === "local" ? "Local" : "Deployed";
}

function iconFor(env: ForgeNavEnv) {
  if (env.source === "checkout") return CircleDashed;
  return env.lifecycle === "local" ? Cpu : Cloud;
}

/** Local first: it is the one a developer opens most. */
const SECTION_ORDER = ["Local", "Deployed", "Not registered"];

export interface ForgeShellProps {
  /** The current pathname; the matching nav entry is marked active. */
  activePath: string;
  /** The environments to list. Empty while unknown. */
  envs?: ForgeNavEnv[];
  /** The backend list has not answered yet — the nav shows skeleton rows. */
  envsLoading?: boolean;
  /**
   * Appended to each nav href. The real layout passes the project so context
   * survives navigation; a preview passes nothing.
   */
  search?: string;
  /** The scope control under the brand — ForgeLayout passes the project switcher. */
  scope?: ReactNode;
  /** The exit, in the brand row — ForgeLayout passes ForgeCloseButton. */
  exit?: ReactNode;
  /**
   * A top bar. The product passes none (each page owns its header); the dev
   * preview harnesses put their case pickers here.
   */
  headerContent?: ReactNode;
  children: ReactNode;
}

export function ForgeShell({
  activePath,
  envs = [],
  envsLoading = false,
  search,
  scope,
  exit,
  headerContent,
  children,
}: ForgeShellProps) {
  // The sidebar's brand row spans the window's leading edge, so it is what has
  // to clear the macOS traffic lights — and, with no top bar, it is also the
  // window's drag handle.
  const { trafficLightPadding, dragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });

  const navItems = useMemo(() => {
    const withSearch = (path: string) => (search ? `${path}?${search}` : path);
    const sorted = [...envs].sort(
      (a, b) => SECTION_ORDER.indexOf(navSectionOf(a)) - SECTION_ORDER.indexOf(navSectionOf(b))
    );
    return [
      {
        label: "Overview",
        href: withSearch(FORGE_OVERVIEW_PATH),
        active: activePath === FORGE_OVERVIEW_PATH || activePath === `${FORGE_OVERVIEW_PATH}/`,
        section: "Project",
        icon: <LayoutGrid className="h-4 w-4" aria-hidden="true" />,
      },
      {
        label: "Domains",
        href: withSearch(FORGE_DOMAINS_PATH),
        active: activePath === FORGE_DOMAINS_PATH,
        section: "Project",
        icon: <Globe className="h-4 w-4" aria-hidden="true" />,
      },
      ...sorted.map((env) => {
        const Icon = iconFor(env);
        const path = forgeEnvPath(env.name);
        return {
          label: env.name,
          href: withSearch(path),
          // decodeURI so an env whose name needed escaping still matches.
          active: decodeURI(activePath) === decodeURI(path),
          section: navSectionOf(env),
          icon: <Icon className="h-4 w-4" aria-hidden="true" />,
          trailing: env.queuedOn ? <QueuedMark env={env.name} on={env.queuedOn} /> : undefined,
        };
      }),
    ];
  }, [activePath, envs, search]);

  return (
    /*
     * `forge-ui` is REQUIRED, not cosmetic. Inside it forge's `accent` token
     * resolves to reliant's primary action color; outside it the same token is
     * reliant's muted hover tint, so every accent-colored forge component —
     * the active nav item, focus rings, primary buttons — would render as a
     * barely-visible grey. See the long comment in src/index.css.
     */
    <div className="forge-ui h-screen w-full" data-testid="forge-shell">
      <SidebarLayout
        brand={
          <div
            className="flex items-center gap-2 transition-[padding] duration-200 ease-in-out"
            style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}
          >
            {exit}
            <span className="text-sm font-semibold tracking-tight text-ink">Deployments</span>
          </div>
        }
        brandAccessory={scope}
        navItems={navItems}
        headerContent={headerContent}
        navAppendix={
          envsLoading && envs.length === 0 ? (
            <div data-testid="forge-nav-loading" aria-label="Loading environments" className="space-y-2 px-3">
              <SkeletonLoader variant="text" count={1} width="70%" />
            </div>
          ) : undefined
        }
      >
        {children}
      </SidebarLayout>
    </div>
  );
}

/** The nav's mark for an environment whose deploy is queued on a person. */
function QueuedMark({ env, on }: { env: string; on: string }) {
  return (
    <span
      className="inline-flex items-center gap-1 rounded-full bg-warning-surface px-1.5 py-0.5 text-2xs font-medium text-warning-ink ring-1 ring-inset ring-warning-border"
      title={`A deploy to ${env} is queued, waiting on ${on}`}
      data-testid={`forge-nav-queued-${env}`}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-warning" aria-hidden="true" />
      Queued
      <span className="sr-only">, waiting on {on}</span>
    </span>
  );
}
