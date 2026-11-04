// Copyright (c) 2025 Reliant Labs

/**
 * WHICH TABS AN ENVIRONMENT PAGE HAS, and which one it opens on.
 *
 * An environment page is the environment AS A CONFIGURED OBJECT: what it is,
 * who can sign in to it, and (for a local one) what is running. Everything an
 * environment HAS that the project also has — its releases, its secrets, its
 * domains, its activity, its workloads and telemetry — is the project's own
 * page, scoped to the environment by the top bar (`?env=`). One place for
 * each, not a second copy per environment; the page links to those scoped
 * pages instead of re-hosting them.
 *
 *   overview  identity              the control plane only. Render with the
 *                                   daemon asleep, from any browser.
 *   running                         the dev stack `forge env up` runs on the
 *                                   daemon's machine. Local envs only.
 *
 * IDENTITY sits beside Overview: "who can sign in to it?" is a question about
 * the running environment, answered by the control plane's identity
 * reconciler with no daemon. A local environment has the tab too, but its IdP
 * is forge's dev IdP on the developer's machine, which reports nothing to the
 * control plane — the tab says so rather than inventing a status.
 *
 * A LOCAL environment leads with what is running. An environment the control
 * plane has no record of leads with Overview, which is where Register lives.
 */

import type { EnvLifecycle } from "@/services/forge/roster";

export type EnvTab = "overview" | "identity" | "running";

export interface EnvTabSpec {
  id: EnvTab;
  label: string;
  /** Whether the tab reads the user's checkout through the daemon. */
  needsDaemon: boolean;
}

const SPECS: Record<EnvTab, EnvTabSpec> = {
  overview: { id: "overview", label: "Overview", needsDaemon: false },
  identity: { id: "identity", label: "Identity", needsDaemon: false },
  running: { id: "running", label: "Running", needsDaemon: true },
};

/**
 * The tabs the page USED to have, and the project page each one became. A
 * link to `?tab=releases` (a bookmark, a notification) lands on that page,
 * scoped to the environment, rather than on a tab that no longer exists.
 */
export const RETIRED_TABS = {
  releases: "releases",
  activity: "activity",
  secrets: "secrets",
  domains: "domains",
} as const;
export type RetiredTab = keyof typeof RETIRED_TABS;

export function retiredTabOf(param: string | undefined): RetiredTab | null {
  return param && Object.prototype.hasOwnProperty.call(RETIRED_TABS, param) ? (param as RetiredTab) : null;
}

export function tabsFor(lifecycle: EnvLifecycle): EnvTabSpec[] {
  const ids: EnvTab[] = lifecycle === "local" ? ["running", "identity"] : ["overview", "identity"];
  return ids.map((id) => SPECS[id]);
}

/** The tab a bare link to this environment opens. */
export function defaultTab(lifecycle: EnvLifecycle): EnvTab {
  return lifecycle === "local" ? "running" : "overview";
}

/** Whether a tab reads the daemon — the page asks for nothing else until one is open. */
export function tabNeedsDaemon(tab: string | undefined): boolean {
  if (!tab) return false;
  return tab in SPECS ? SPECS[tab as EnvTab].needsDaemon : false;
}

/**
 * The URL's `tab` resolved against this environment's tabs. An unknown or
 * inapplicable value falls back to the default rather than rendering nothing —
 * a link to prod's `running` tab, or a retired `live`, `preview`, `changes` or
 * `checks`, still lands somewhere.
 */
export function resolveTab(param: string | undefined, lifecycle: EnvLifecycle): EnvTab {
  const fallback = defaultTab(lifecycle);
  if (!param || param === "live") return fallback;
  return tabsFor(lifecycle).some((tab) => tab.id === param) ? (param as EnvTab) : fallback;
}

/** What goes in the URL for a tab: absent for the default, so the plain link stays plain. */
export function tabParam(tab: EnvTab, lifecycle: EnvLifecycle): EnvTab | undefined {
  return tab === defaultTab(lifecycle) ? undefined : tab;
}
