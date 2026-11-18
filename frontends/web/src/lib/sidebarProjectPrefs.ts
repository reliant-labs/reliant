// Copyright (c) 2025 Reliant Labs

/**
 * A person's sidebar choices about projects, per org: which projects are
 * pinned (always listed, first), which are hidden (left out of the sidebar,
 * still in Settings → Projects and under show-all), and whether the sidebar
 * shows every project rather than only the active ones (lib/sidebarGroups
 * listProjectGroups). It is the user setting `sidebar.projects`, a JSON
 * object keyed by org id, like `provider.org_funding` (lib/orgFunding):
 *
 *   {"<org_id>": {"pinned": ["<project_id>"], "hidden": ["<project_id>"], "show_all": true}}
 *
 * The key "" holds the choices made with no org (no control plane, or a
 * person in none). A project belongs to one org, so a pin or a hide is per
 * org by construction; show-all is the field that needs the key.
 *
 * Pure: no React, no RPCs.
 */

export const SIDEBAR_PROJECTS_KEY = "sidebar.projects";

/** One org's choices, read. */
export interface SidebarProjectPrefs {
  pinned: ReadonlySet<string>;
  hidden: ReadonlySet<string>;
  showAll: boolean;
}

/** One org's stored entry (omitempty fields). */
interface StoredEntry {
  pinned?: string[];
  hidden?: string[];
  show_all?: boolean;
}

/** The parsed setting, by org id ("" for no org). */
export type SidebarProjectSettings = Record<string, StoredEntry>;

export const DEFAULT_SIDEBAR_PROJECT_PREFS: SidebarProjectPrefs = {
  pinned: new Set(),
  hidden: new Set(),
  showAll: false,
};

/** The setting's key for `orgId`: null names no org. */
export function sidebarOrgKey(orgId: string | null | undefined): string {
  return orgId ?? "";
}

function ids(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) return undefined;
  const out = v.filter((id): id is string => typeof id === "string" && id !== "");
  return out.length > 0 ? [...new Set(out)] : undefined;
}

/** Reads the setting's value. Anything unreadable is no entries: every org has the defaults. */
export function parseSidebarProjectSettings(value: string | undefined): SidebarProjectSettings {
  if (!value?.trim()) return {};
  try {
    const raw: unknown = JSON.parse(value);
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
    const out: SidebarProjectSettings = {};
    for (const [orgKey, entry] of Object.entries(raw as Record<string, unknown>)) {
      if (!entry || typeof entry !== "object" || Array.isArray(entry)) continue;
      const e = entry as Record<string, unknown>;
      const pinned = ids(e.pinned);
      const hidden = ids(e.hidden);
      out[orgKey] = {
        ...(pinned ? { pinned } : {}),
        ...(hidden ? { hidden } : {}),
        ...(e.show_all === true ? { show_all: true } : {}),
      };
    }
    return out;
  } catch {
    return {};
  }
}

/** `orgId`'s choices, the defaults filling anything unset. */
export function sidebarProjectPrefsFor(
  settings: SidebarProjectSettings,
  orgId: string | null | undefined,
): SidebarProjectPrefs {
  const e = settings[sidebarOrgKey(orgId)];
  if (!e) return DEFAULT_SIDEBAR_PROJECT_PREFS;
  return {
    pinned: new Set(e.pinned ?? []),
    hidden: new Set(e.hidden ?? []),
    showAll: e.show_all === true,
  };
}

/** A change to one org's choices. */
export type SidebarProjectChange =
  | { kind: "pin"; projectId: string; on: boolean }
  | { kind: "hide"; projectId: string; on: boolean }
  | { kind: "showAll"; on: boolean };

function toggled(list: readonly string[] | undefined, id: string, on: boolean): string[] | undefined {
  const rest = (list ?? []).filter((x) => x !== id);
  const next = on ? [...rest, id] : rest;
  return next.length > 0 ? next : undefined;
}

/**
 * The setting with `change` applied to `orgId`'s entry. Pinning a project
 * unhides it and hiding one unpins it: a project is one or the other. An
 * entry left with nothing in it is removed, so the defaults store nothing.
 */
export function withSidebarProjectChange(
  settings: SidebarProjectSettings,
  orgId: string | null | undefined,
  change: SidebarProjectChange,
): SidebarProjectSettings {
  const orgKey = sidebarOrgKey(orgId);
  const prev = settings[orgKey] ?? {};
  let pinned = prev.pinned;
  let hidden = prev.hidden;
  let showAll = prev.show_all === true;
  switch (change.kind) {
    case "pin":
      pinned = toggled(pinned, change.projectId, change.on);
      if (change.on) hidden = toggled(hidden, change.projectId, false);
      break;
    case "hide":
      hidden = toggled(hidden, change.projectId, change.on);
      if (change.on) pinned = toggled(pinned, change.projectId, false);
      break;
    case "showAll":
      showAll = change.on;
      break;
  }
  const out: SidebarProjectSettings = { ...settings };
  const next: StoredEntry = {
    ...(pinned ? { pinned } : {}),
    ...(hidden ? { hidden } : {}),
    ...(showAll ? { show_all: true } : {}),
  };
  if (Object.keys(next).length > 0) out[orgKey] = next;
  else delete out[orgKey];
  return out;
}
