// Copyright (c) 2025 Reliant Labs

/**
 * The explorer's own reads, beside the shared hooks (hooks/observability-queries):
 * the panel it runs, debounced so typing a formula is one query rather than one
 * per keystroke, and the SQL tab's explain, which keeps the window it compiled
 * for so "Open as SQL panel" can turn that window back into `{from}`/`{to}`.
 */

import { useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { panelWindow, queryPanelRead, type PanelOffset } from "@/hooks/observability-queries";
import { useQueryPanel } from "@/hooks/controlplane/observability-service-hooks_gen";
import { useDebounce } from "@/hooks/useDebounce";
import { keepPreviousInOrg } from "@/lib/orgScope";
import { queryPanelRequest, toPanelResult } from "@/services/controlPlane/observability/api";
import { obsRetry, obsRetryDelay } from "@/services/controlPlane/observability/errors";
import { obsCall } from "@/services/controlPlane/observability/obsInterceptors";
import type { ObsScope, Panel, PanelResult, TimeWindow } from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";

import { canonicalJSON } from "./model/canonical";

/** How long the builder waits after the last edit before it asks (DESIGN §3.5). */
export const QUERY_DEBOUNCE_MS = 400;

/**
 * `panel`, once it has stopped changing for QUERY_DEBOUNCE_MS. Keyed by the
 * panel's canonical JSON, so a re-render that builds an equal panel is no edit,
 * and the cache key (canonical panel + scope + range) is stable.
 */
export function useDebouncedPanel(panel: Panel | null): Panel | null {
  const text = panel ? canonicalJSON(panel) : "";
  const settled = useDebounce(text, QUERY_DEBOUNCE_MS);
  return useMemo(() => (settled ? (JSON.parse(settled) as Panel) : null), [settled]);
}

export interface ExplorerPanelOptions {
  enabled: boolean;
  /** A comparison: where the window sits back in time. */
  offset?: PanelOffset;
}

/** No scope yet: the request of a read that is never sent. */
const NO_SCOPE: ObsScope = { orgId: "", envIds: [], filters: [] };

/** Marks each read, as it starts, for the control-plane transport's read stage (obsInterceptors.ts). */
const obsRead = () => obsCall();

/**
 * The explorer's panel, run (QueryPanel's generated read). Keyed by its
 * request, as the dashboards' panels are, so a saved panel and the explorer
 * share answers. Unlike a dashboard tile, the previous answer stays on screen
 * while an edited query runs — the chart does not blink on every edit — and
 * `isPlaceholderData` says it is stale.
 */
export function useExplorerPanelQuery(scope: ObsScope | null, range: RangePreset, panel: Panel | null, options: ExplorerPanelOptions) {
  const window = panelWindow(range, undefined, options.offset);
  const request = queryPanelRequest({ ...(scope ?? NO_SCOPE), window, panel: panel ?? EMPTY_PANEL, services: [], stepSeconds: 0, groupBy: [] });
  return useQuery({
    ...queryPanelRead(request, window),
    enabled: options.enabled && !!scope && !!panel,
    // The previous answer, but never another org's (lib/orgScope).
    placeholderData: keepPreviousInOrg(scope?.orgId ?? null),
  });
}

const EMPTY_PANEL: Panel = { id: "", title: "", kind: "line", signal: "metrics", series: [] };

export interface Explained {
  result: PanelResult;
  /** The window the statements were compiled for. */
  window: TimeWindow;
}

/**
 * QueryPanel with `explain`: the statements the panel compiles to, run nowhere
 * (it costs no ClickHouse). Asked over absolute bounds, resolved when the
 * panel or range changes: "Open as SQL panel" turns the compiled window back
 * into `{from}`/`{to}`, so it must know the exact window the server compiled for.
 */
export function useExplainPanel(scope: ObsScope | null, range: RangePreset, panel: Panel | null, enabled: boolean) {
  // eslint-disable-next-line react-hooks/exhaustive-deps -- a new panel is a new read, over the window as of now
  const window = useMemo(() => resolveWindow(range), [range, panel]);
  const select = useCallback((res: Parameters<typeof toPanelResult>[0]): Explained => ({ result: toPanelResult(res, window), window }), [window]);
  return useQueryPanel(queryPanelRequest({ ...(scope ?? NO_SCOPE), window, panel: panel ?? EMPTY_PANEL, services: [], stepSeconds: 0, explain: true }), {
    select,
    enabled: enabled && !!scope && !!panel,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
    callOptions: obsRead,
  });
}
