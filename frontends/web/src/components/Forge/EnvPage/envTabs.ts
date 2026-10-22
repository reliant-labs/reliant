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
 *   overview  identity
 *   releases  activity
 *   secrets   domains               the control plane only. Render with the
 *                                   daemon asleep, from any browser.
 *   running                         the dev stack `forge env up` runs on the
 *                                   daemon's machine. Local envs only.
 *
 * There is no per-branch Changes or Checks tab: a branch's diff and checks are
 * what the user's CI builds report, not something this page re-derives.
 *
 * RELEASES AND ACTIVITY ARE TWO TABS ON PURPOSE. A release is a thing you
 * pick and compare ("what's on prod, what was before it"); activity is what
 * HAPPENED — promotions and what the platform observed after each. Interleaved
 * in one list, the observations pushed the releases apart and a burst of them
 * buried the history a reader opened the tab for.
 *
 * There is no Observability tab: an environment's workloads, their health and
 * its telemetry are the project's Overview and Observability pages, scoped to
 * it (decision D1) — one place for each, not a second copy per environment.
 *
 * IDENTITY sits beside Overview: "who can sign in to it?" is a
 * question about the running environment, answered by the control plane's
 * identity reconciler with no daemon. A local environment has the tab too, but
 * its IdP is forge's dev IdP on the developer's machine, which reports nothing
 * to the control plane — the tab says so rather than inventing a status.
 *
 * A LOCAL environment has no releases (it runs the working tree) and leads
 * with what is running. An environment the control plane has no record of
 * leads with Overview, which is where Register lives.
 */

import type { EnvLifecycle } from "@/services/forge/roster";

export type EnvTab =
  | "overview"
  | "identity"
  | "running"
  | "releases"
  | "activity"
  | "secrets"
  | "domains";

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
  releases: { id: "releases", label: "Releases", needsDaemon: false },
  activity: { id: "activity", label: "Activity", needsDaemon: false },
  secrets: { id: "secrets", label: "Secrets", needsDaemon: false },
  domains: { id: "domains", label: "Domains", needsDaemon: false },
};

export function tabsFor(lifecycle: EnvLifecycle): EnvTabSpec[] {
  const ids: EnvTab[] =
    lifecycle === "local"
      ? ["running", "identity", "secrets"]
      : ["overview", "identity", "releases", "activity", "secrets", "domains"];
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
