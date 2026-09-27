// Copyright (c) 2025 Reliant Labs

/**
 * The forge surface's CHROME — sidebar, nav, header slot — with no data,
 * no routing and no project resolution.
 *
 * WHY THIS IS A SEPARATE COMPONENT, and it is not a refactor for tidiness.
 *
 * The forge screens are hard to look at while building them: every route is
 * behind auth, and the states that matter most visually (a destroyed secret
 * version, a cluster that could not be reached, a declared-but-never-set key)
 * are the ones hardest to produce on demand against a live backend. So each
 * screen grew a dev-only preview harness rendering it against fabricated data.
 *
 * Those harnesses rendered the screen BARE — no sidebar, no header. That made
 * them actively misleading: a screenshot from a preview looks like the product
 * and is not, and the difference is invisible unless you already know which
 * URL produced it. It cost two review cycles here, both spent on "where is the
 * left nav?" when the nav was present in the app the whole time.
 *
 * The fix is structural rather than a note in a README: the previews render
 * THIS, the same component the real layout renders. A harness can no longer
 * drift from the product chrome, because there is only one copy of it.
 *
 * ForgeLayout owns everything stateful — auth, the project-resolution ladder,
 * the Escape binding, the router Outlet. This owns only what you can see.
 *
 * ── THE NAV IS THE ENVIRONMENT LIST ─────────────────────────────────────────
 *
 * It used to name one screen per forge COMMAND (Releases, Environments,
 * Secrets, Status). A reader does not think in commands; they think "prod".
 * So the nav is the Overview plus one entry per environment, and everything
 * about an environment — its workloads, secrets, releases and dev stack — is
 * on that environment's page. The environment list is passed in rather than
 * fetched, for the reason above: a preview has to be able to draw it.
 */

import { useMemo, type ReactNode } from "react";
import { Cloud, Cpu, LayoutGrid, Server } from "lucide-react";

import SidebarLayout from "@/components/forge-ui/sidebar_layout";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import type { EnvWhere } from "@/services/forge/environments";

/** The Overview's path. The one fixed destination; everything else is an environment. */
export const FORGE_OVERVIEW_PATH = "/forge";

/** The path of one environment's page. */
export function forgeEnvPath(env: string): string {
  return `/forge/env/${encodeURIComponent(env)}`;
}

export interface ForgeNavEnv {
  name: string;
  where: EnvWhere;
}

/** The icon says where it runs, so the list is scannable before any page loads. */
function iconFor(where: EnvWhere) {
  switch (where) {
    case "local":
      return Cpu;
    case "cloud":
      return Cloud;
    default:
      return Server;
  }
}

export interface ForgeShellProps {
  /** The current pathname; the matching nav entry is marked active. */
  activePath: string;
  /** The environments to list under the Overview. Empty while unknown. */
  envs?: ForgeNavEnv[];
  /**
   * Appended to each nav href. The real layout passes the project so context
   * survives navigation; a preview passes nothing.
   */
  search?: string;
  /** The top bar. ForgeLayout passes ForgeHeader; a preview passes its controls. */
  headerContent?: ReactNode;
  children: ReactNode;
}

export function ForgeShell({ activePath, envs = [], search, headerContent, children }: ForgeShellProps) {
  // The sidebar's brand row spans the window's leading edge, so it — not the
  // header bar — is what has to clear the macOS traffic lights.
  const { trafficLightPadding } = useTitleBarChrome({ collapsedPadding: "0px" });

  const navItems = useMemo(() => {
    const withSearch = (path: string) => (search ? `${path}?${search}` : path);
    return [
      {
        label: "Overview",
        href: withSearch(FORGE_OVERVIEW_PATH),
        active: activePath === FORGE_OVERVIEW_PATH || activePath === `${FORGE_OVERVIEW_PATH}/`,
        section: "Project",
        icon: <LayoutGrid className="h-4 w-4" aria-hidden="true" />,
      },
      ...envs.map((env) => {
        const Icon = iconFor(env.where);
        const path = forgeEnvPath(env.name);
        return {
          label: env.name,
          href: withSearch(path),
          // decodeURI so an env whose name needed escaping still matches.
          active: decodeURI(activePath) === decodeURI(path),
          section: "Environments",
          icon: <Icon className="h-4 w-4" aria-hidden="true" />,
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
            className="flex items-center transition-[padding] duration-200 ease-in-out"
            style={{ paddingLeft: trafficLightPadding }}
          >
            <span className="text-sm font-semibold tracking-tight text-ink">forge</span>
          </div>
        }
        navItems={navItems}
        headerContent={headerContent}
      >
        {children}
      </SidebarLayout>
    </div>
  );
}
