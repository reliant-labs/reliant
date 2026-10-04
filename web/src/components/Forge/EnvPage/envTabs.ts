// Copyright (c) 2025 Reliant Labs

/**
 * WHICH TABS AN ENVIRONMENT PAGE HAS, and which one it opens on.
 *
 * The tabs follow the reader's question, not the data source. The old page had
 * two tabs, Live and Preview, which was the daemon boundary drawn on screen: to
 * find a release's history you had to know it lived on the control plane, and
 * to find the deploy button you had to know forge plans on your daemon.
 *
 * The boundary still holds — per TAB:
 *
 *   overview  releases  secrets   the control plane only. Render with the
 *                                 daemon asleep, from any browser.
 *   running                       the dev stack `forge env up` runs on the
 *                                 daemon's machine. Local envs only.
 *   changes   checks              read the user's checkout through the daemon.
 *
 * A LOCAL environment has no releases (it runs the working tree) and leads
 * with what is running. An environment the control plane has no record of
 * leads with Overview, which is where Register lives.
 */

import type { EnvLifecycle } from "@/services/forge/roster";

export type EnvTab = "overview" | "running" | "releases" | "secrets" | "changes" | "checks";

export interface EnvTabSpec {
  id: EnvTab;
  label: string;
  /** Whether the tab reads the user's checkout through the daemon. */
  needsDaemon: boolean;
}

const SPECS: Record<EnvTab, EnvTabSpec> = {
  overview: { id: "overview", label: "Overview", needsDaemon: false },
  running: { id: "running", label: "Running", needsDaemon: true },
  releases: { id: "releases", label: "Releases", needsDaemon: false },
  secrets: { id: "secrets", label: "Secrets", needsDaemon: false },
  changes: { id: "changes", label: "Changes", needsDaemon: true },
  checks: { id: "checks", label: "Checks", needsDaemon: true },
};

export function tabsFor(lifecycle: EnvLifecycle): EnvTabSpec[] {
  const ids: EnvTab[] =
    lifecycle === "local"
      ? ["running", "secrets", "changes", "checks"]
      : ["overview", "releases", "secrets", "changes", "checks"];
  return ids.map((id) => SPECS[id]);
}

/** The tab a bare link to this environment opens. */
export function defaultTab(lifecycle: EnvLifecycle): EnvTab {
  return lifecycle === "local" ? "running" : "overview";
}

/**
 * The URL's `tab` resolved against this environment's tabs. An unknown or
 * inapplicable value falls back to the default rather than rendering nothing —
 * a link to prod's `running` tab, or a retired `live`, still lands somewhere.
 */
export function resolveTab(param: string | undefined, lifecycle: EnvLifecycle): EnvTab {
  const fallback = defaultTab(lifecycle);
  if (!param || param === "live") return fallback;
  const wanted = param === "preview" ? "changes" : param;
  return tabsFor(lifecycle).some((tab) => tab.id === wanted) ? (wanted as EnvTab) : fallback;
}

/** What goes in the URL for a tab: absent for the default, so the plain link stays plain. */
export function tabParam(tab: EnvTab, lifecycle: EnvLifecycle): EnvTab | undefined {
  return tab === defaultTab(lifecycle) ? undefined : tab;
}
