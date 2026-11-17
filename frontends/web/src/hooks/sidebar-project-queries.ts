// Copyright (c) 2025 Reliant Labs

import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { useOrgScope } from "../lib/orgScope";
import { readSetting, upsertStringSetting } from "../lib/settingsPersistence";
import {
  DEFAULT_SIDEBAR_PROJECT_PREFS,
  parseSidebarProjectSettings,
  SIDEBAR_PROJECTS_KEY,
  sidebarProjectPrefsFor,
  withSidebarProjectChange,
  type SidebarProjectChange,
  type SidebarProjectPrefs,
  type SidebarProjectSettings,
} from "../lib/sidebarProjectPrefs";
import { settingsSync } from "../services/settingsSync";

export const sidebarProjectPrefsKey = ["sidebar-project-prefs"] as const;

/**
 * The setting as last read or written in this tab, for a reader outside React
 * (the keyboard navigation store): the query's data, else the settings cache.
 */
export function cachedSidebarProjectSettings(qc: QueryClient): SidebarProjectSettings {
  return (
    qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey) ??
    parseSidebarProjectSettings(settingsSync.getSettingFromCache(SIDEBAR_PROJECTS_KEY)?.value)
  );
}

export interface SidebarProjectPrefsView {
  /** The active org's choices; the defaults until the setting is read. */
  prefs: SidebarProjectPrefs;
  setPinned: (projectId: string, on: boolean) => void;
  setHidden: (projectId: string, on: boolean) => void;
  setShowAll: (on: boolean) => void;
}

/**
 * The person's sidebar project choices in the tab's org (the user setting
 * `sidebar.projects`, lib/sidebarProjectPrefs), and setters that write the
 * whole setting back. A change shows at once; a refused write puts back what
 * the server holds.
 */
export function useSidebarProjectPrefs(): SidebarProjectPrefsView {
  const qc = useQueryClient();
  const { ready, orgId } = useOrgScope();
  const q = useQuery({
    queryKey: sidebarProjectPrefsKey,
    queryFn: async () => {
      const read = await readSetting(SIDEBAR_PROJECTS_KEY);
      return parseSidebarProjectSettings(read.status === "found" ? read.value : undefined);
    },
    staleTime: 30_000,
  });
  const m = useMutation({
    mutationFn: async (next: SidebarProjectSettings) => {
      await upsertStringSetting(SIDEBAR_PROJECTS_KEY, JSON.stringify(next));
    },
    onError: () => {
      void qc.invalidateQueries({ queryKey: sidebarProjectPrefsKey });
    },
  });
  const { mutate } = m;
  // Each change names its org explicitly: the tab's org when the change
  // starts, never one read later. It applies to the setting as it is NOW, not
  // as it was when this render ran, so two quick toggles both land. Until the
  // tab's org is known there is no org to write the change under.
  const apply = useCallback(
    (change: SidebarProjectChange, changeOrgId: string | null) => {
      const next = withSidebarProjectChange(cachedSidebarProjectSettings(qc), changeOrgId, change);
      // A read in flight would land over the change.
      void qc.cancelQueries({ queryKey: sidebarProjectPrefsKey });
      qc.setQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey, next);
      mutate(next);
    },
    [qc, mutate],
  );
  const prefs = useMemo(
    () => (ready ? sidebarProjectPrefsFor(q.data ?? {}, orgId) : DEFAULT_SIDEBAR_PROJECT_PREFS),
    [q.data, ready, orgId],
  );
  return useMemo(() => {
    const change = (c: SidebarProjectChange) => {
      if (ready) apply(c, orgId);
    };
    return {
      prefs,
      setPinned: (projectId, on) => change({ kind: "pin", projectId, on }),
      setHidden: (projectId, on) => change({ kind: "hide", projectId, on }),
      setShowAll: (on) => change({ kind: "showAll", on }),
    };
  }, [prefs, apply, ready, orgId]);
}
