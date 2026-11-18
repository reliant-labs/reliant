// Copyright (c) 2025 Reliant Labs

import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import { useOrgScope } from "../lib/orgScope";
import { readSettingFromServer, writeStringSetting } from "../lib/settingsPersistence";
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

/** The stored document, read from the server; a failed read throws. */
async function readStoredSettings(): Promise<SidebarProjectSettings> {
  const read = await readSettingFromServer(SIDEBAR_PROJECTS_KEY);
  if (read.status === "error") throw new Error("Could not load your sidebar project choices");
  return parseSidebarProjectSettings(read.status === "found" ? read.value : undefined);
}

interface ChangeVars {
  change: SidebarProjectChange;
  /** The org the change was made in, captured when it started. */
  orgId: string | null;
}

export interface SidebarProjectPrefsView {
  /** The active org's choices; the defaults until the setting is read. */
  prefs: SidebarProjectPrefs;
  /** Whether a change can be saved: the org is known and the setting was read. */
  writable: boolean;
  /** The setting could not be read: the choices shown are the defaults. */
  loadFailed: boolean;
  /** Read the setting again after a failure. */
  retry: () => void;
  setPinned: (projectId: string, on: boolean) => void;
  setHidden: (projectId: string, on: boolean) => void;
  setShowAll: (on: boolean) => void;
}

/**
 * The person's sidebar project choices in the tab's org (the user setting
 * `sidebar.projects`, lib/sidebarProjectPrefs), and setters.
 *
 * The setting is one document holding every org's entry, so a write rewrites
 * all of it. Two rules keep a write from erasing anything:
 *   - No change is accepted until the document has been read: a failed read
 *     is an error with a retry, never "nothing stored", which the next change
 *     would write over every org's choices.
 *   - Each change re-reads the stored document from the server right before
 *     it writes, and changes only its own org's entry in what it read. The
 *     settings API has no compare-and-set (no version or precondition on a
 *     write), so this narrows a race between tabs to the read-to-write gap
 *     rather than closing it. Changes from this tab run one at a time.
 *
 * A change shows at once. A write that does not land (writeStringSetting
 * rejects) puts back what was shown before it and reads the server again.
 */
export function useSidebarProjectPrefs(): SidebarProjectPrefsView {
  const qc = useQueryClient();
  const { ready, orgId } = useOrgScope();
  const q = useQuery({
    queryKey: sidebarProjectPrefsKey,
    queryFn: readStoredSettings,
    staleTime: 30_000,
    retry: false,
  });
  const m = useMutation({
    // One at a time, so each re-read sees the write before it.
    scope: { id: "sidebar-project-prefs" },
    mutationFn: async ({ change, orgId: changeOrgId }: ChangeVars) => {
      const next = withSidebarProjectChange(await readStoredSettings(), changeOrgId, change);
      await writeStringSetting(SIDEBAR_PROJECTS_KEY, JSON.stringify(next));
      return next;
    },
    onMutate: async ({ change, orgId: changeOrgId }: ChangeVars) => {
      // A read in flight would land over the change.
      await qc.cancelQueries({ queryKey: sidebarProjectPrefsKey });
      const prev = qc.getQueryData<SidebarProjectSettings>(sidebarProjectPrefsKey);
      if (prev) qc.setQueryData(sidebarProjectPrefsKey, withSidebarProjectChange(prev, changeOrgId, change));
      return { prev };
    },
    onSuccess: (stored) => {
      qc.setQueryData(sidebarProjectPrefsKey, stored);
    },
    onError: (_error, _vars, ctx) => {
      if (ctx?.prev) qc.setQueryData(sidebarProjectPrefsKey, ctx.prev);
      void qc.invalidateQueries({ queryKey: sidebarProjectPrefsKey });
    },
  });
  const { mutate } = m;
  const writable = ready && q.isSuccess;
  const change = useCallback(
    (c: SidebarProjectChange) => {
      // The org is the tab's when the change starts, never one read later.
      if (writable) mutate({ change: c, orgId });
    },
    [writable, mutate, orgId],
  );
  const prefs = useMemo(
    () => (ready && q.data ? sidebarProjectPrefsFor(q.data, orgId) : DEFAULT_SIDEBAR_PROJECT_PREFS),
    [q.data, ready, orgId],
  );
  const { refetch } = q;
  return useMemo(
    () => ({
      prefs,
      writable,
      loadFailed: q.isError && !q.data,
      retry: () => void refetch(),
      setPinned: (projectId, on) => change({ kind: "pin", projectId, on }),
      setHidden: (projectId, on) => change({ kind: "hide", projectId, on }),
      setShowAll: (on) => change({ kind: "showAll", on }),
    }),
    [prefs, writable, q.isError, q.data, refetch, change],
  );
}
