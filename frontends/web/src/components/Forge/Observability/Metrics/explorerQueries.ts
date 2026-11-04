// Copyright (c) 2025 Reliant Labs

/**
 * The explorer's own reads, beside the shared hooks (hooks/observability-queries):
 * the panel it runs, debounced so typing a formula is one query rather than one
 * per keystroke, and the SQL tab's explain, which keeps the window it compiled
 * for so "Open as SQL panel" can turn that window back into `{from}`/`{to}`.
 */

import { useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { metricKeys, obsKeys, useObsReadApi } from "@/hooks/observability-queries";
import { useDebounce } from "@/hooks/useDebounce";
import { obsRetry, obsRetryDelay } from "@/services/controlPlane/observability/errors";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
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
  /** A comparison: how far back the window is shifted, resolved at fetch time, and its cache key. */
  offsetKey?: string;
  offsetMs?: () => number;
}

/**
 * The explorer's panel, run (QueryPanel). The key is the dashboards' own
 * (`obsKeys.panel`: scope + range + canonical panel), so a saved panel and the
 * explorer share answers. Unlike a dashboard tile, the previous answer stays on
 * screen while an edited query runs — the chart does not blink on every edit —
 * and `isPlaceholderData` says it is stale.
 */
export function useExplorerPanelQuery(scope: ObsScope | null, range: RangePreset, panel: Panel | null, options: ExplorerPanelOptions) {
  const api = useObsReadApi();
  return useQuery({
    queryKey: obsKeys.panel(scope, range, panel ?? EMPTY_PANEL, [], [], options.offsetKey ?? ""),
    queryFn: ({ signal }) => {
      const offset = options.offsetMs?.() ?? 0;
      return api.queryPanel({ ...(scope as ObsScope), window: resolveWindow(range, Date.now() - offset), panel: panel as Panel, services: [], stepSeconds: 0, groupBy: [] }, { signal });
    },
    enabled: options.enabled && !!scope && !!panel,
    // Every range is relative ("the last hour"), so an answer is stale once time has moved on.
    staleTime: 30_000,
    placeholderData: keepPreviousData,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}

const EMPTY_PANEL: Panel = { id: "", title: "", kind: "line", signal: "metrics", series: [] };

export interface Explained {
  result: PanelResult;
  /** The window the statements were compiled for. */
  window: TimeWindow;
}

/** QueryPanel with `explain`: the statements the panel compiles to, run nowhere (it costs no ClickHouse). */
export function useExplainPanel(scope: ObsScope | null, range: RangePreset, panel: Panel | null, enabled: boolean) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: [...metricKeys.all(scope), "explain", range, panel] as const,
    queryFn: async ({ signal }): Promise<Explained> => {
      const window = resolveWindow(range);
      const result = await api.queryPanel({ ...(scope as ObsScope), window, panel: panel as Panel, services: [], stepSeconds: 0, explain: true }, { signal });
      return { result, window };
    },
    enabled: enabled && !!scope && !!panel,
    staleTime: 30_000,
    retry: obsRetry, retryDelay: obsRetryDelay,
  });
}
