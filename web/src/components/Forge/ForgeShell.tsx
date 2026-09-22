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
 */

import { useMemo, type ReactNode } from "react";
import { KeyRound, Layers, Rocket, ShieldCheck } from "lucide-react";

import SidebarLayout from "@/components/forge-ui/sidebar_layout";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";

/**
 * The nav, in one place. Exported because ForgeLayout builds hrefs from it and
 * a preview needs the same labels — two copies would drift the moment a screen
 * is added.
 *
 * `carriesEnv` marks the destinations for which an environment is meaningful.
 * Releases is project-scoped, so carrying `env` there would put a param in the
 * URL that its route schema strips anyway.
 */
export const FORGE_NAV = [
  { to: "/forge/environments", label: "Environments", icon: Layers, carriesEnv: true },
  { to: "/forge/topology", label: "Releases", icon: Rocket, carriesEnv: false },
  { to: "/forge/secrets", label: "Secrets", icon: KeyRound, carriesEnv: true },
  { to: "/forge/status", label: "Status", icon: ShieldCheck, carriesEnv: true },
] as const;

export interface ForgeShellProps {
  /** Which nav destination is current, by `to`. */
  activePath: string;
  /**
   * Appended to each nav href. The real layout passes project/env so context
   * survives a tab change; a preview passes nothing.
   */
  searchFor?: (item: (typeof FORGE_NAV)[number]) => string;
  /** The top bar. ForgeLayout passes ForgeHeader; a preview passes its controls. */
  headerContent?: ReactNode;
  children: ReactNode;
}

export function ForgeShell({
  activePath,
  searchFor,
  headerContent,
  children,
}: ForgeShellProps) {
  // The sidebar's brand row spans the window's leading edge, so it — not the
  // header bar — is what has to clear the macOS traffic lights.
  const { trafficLightPadding } = useTitleBarChrome({ collapsedPadding: "0px" });

  const navItems = useMemo(
    () =>
      FORGE_NAV.map((item) => {
        const query = searchFor?.(item) ?? "";
        const Icon = item.icon;
        return {
          label: item.label,
          href: query ? `${item.to}?${query}` : item.to,
          active: activePath === item.to,
          section: "Project",
          icon: <Icon className="h-4 w-4" aria-hidden="true" />,
        };
      }),
    [activePath, searchFor]
  );

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
