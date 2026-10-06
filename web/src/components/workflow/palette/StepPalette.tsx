// Copyright (c) 2025 Reliant Labs

/**
 * StepPalette — the one place to add anything to a workflow
 * (research/INTEGRATIONS_V1_BRIEF.md §3a): core node types as a "Built-in"
 * group, and integration actions and triggers from the catalog.
 *
 * Two modes, one list:
 *
 *  - Browsing (nothing typed): the built-ins, with the low-level agent
 *    building blocks folded under "Advanced", then every integration as one
 *    row with its logo — the ones the user can use now first — from
 *    ListCatalogIntegrations. Choosing an integration expands its actions (or
 *    triggers) beneath it, from SearchCatalog filtered to that integration.
 *  - Searching: every built-in and catalog entry that matches, ranked by the
 *    server.
 *
 * Built for a catalog of hundreds: the server does the matching and the
 * grouping, both lists are paged by token as the user scrolls or arrows
 * down, and nothing fetches the whole catalog. Keyboard-first as an ARIA 1.2
 * combobox: the input keeps focus, ↑/↓ move the active option, ↵ adds it (or
 * expands an integration), →/← expand and collapse while browsing, ⇧↵
 * connects an integration that is not connected, Esc closes.
 */

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, ChevronDown, ChevronRight, CornerDownLeft, Loader2, Search, X } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactElement } from "react";
import { createPortal } from "react-dom";

import Badge from "../../forge-ui/badge";
import { IntegrationLogoTile } from "../../icons/IntegrationLogo";
import { catalogSearchGrpc, type CatalogEntrySummary, type CatalogKind } from "../../../api/catalog-search-grpc";
import { ensureNodesCached, getNodeBgColor, getNodeIcon } from "../../../lib/node-metadata";
import { cn } from "../../../lib/utils";
import {
  BUILTIN_TRIGGER_ITEMS,
  builtinItemsFromNodes,
  entryCountLabel,
  matchesBuiltin,
  paletteItemKey,
  paletteItemLabel,
  type BuiltinPaletteItem,
  type BuiltinTriggerPaletteItem,
  type PaletteItem,
} from "./paletteItems";
import { catalogSearchKeys, useCatalogIntegrations, useCatalogSearch } from "./useCatalogSearch";

export interface StepPaletteProps {
  open: boolean;
  onClose: () => void;
  /** Which kind the palette opens on. */
  initialKind?: CatalogKind;
  /** Offer the Steps / Triggers switch. Off where only one kind can be added. */
  allowKindSwitch?: boolean;
  /**
   * Where to open, for the sidebar's shortcuts: one integration expanded and
   * active, or the top of the integrations list. Default: the first row.
   */
  initialFocus?: PaletteFocus;
  onChooseBuiltin: (type: string) => void;
  onChooseAction: (entry: CatalogEntrySummary) => void;
  onChooseTrigger?: (entry: CatalogEntrySummary) => void;
  onChooseBuiltinTrigger?: (source: BuiltinTriggerPaletteItem["source"]) => void;
  /** Start connecting the entry's integration. */
  onConnect: (entry: CatalogEntrySummary) => void;
}

export type PaletteFocus = { integration: string } | "integrations";

export function StepPalette(props: StepPaletteProps) {
  if (!props.open) return null;
  return createPortal(<StepPaletteBody {...props} />, document.body);
}

/** "engineering" → "Engineering". Categories are manifest ids, not copy. */
export function categoryLabel(category: string): string {
  if (!category) return "Other";
  return category.charAt(0).toUpperCase() + category.slice(1).replace(/[_-]/g, " ");
}

/** Fetch more when the active option comes within this many of the end. */
const PREFETCH_THRESHOLD = 4;

function StepPaletteBody({
  onClose,
  initialKind = "action",
  allowKindSwitch = false,
  initialFocus,
  onChooseBuiltin,
  onChooseAction,
  onChooseTrigger,
  onChooseBuiltinTrigger,
  onConnect,
}: StepPaletteProps) {
  const ids = useId();
  const listId = `${ids}-list`;
  const optionId = (index: number) => `${ids}-opt-${index}`;
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const restoreFocusRef = useRef<Element | null>(typeof document !== "undefined" ? document.activeElement : null);
  const queryClient = useQueryClient();

  const [query, setQuery] = useState("");
  const [kind, setKind] = useState<CatalogKind>(initialKind);
  const [category, setCategory] = useState("");
  // The active option is tracked by key, not index: expanding one integration
  // collapses another above it, which moves every row below.
  const initialIntegration = typeof initialFocus === "object" ? initialFocus.integration : null;
  const [activeKey, setActiveKey] = useState<string | null>(initialIntegration ? `integration:${initialIntegration}` : null);
  const [expandedIntegration, setExpandedIntegration] = useState<string | null>(initialIntegration);
  // "integrations" lands on the first integration once the list arrives, once.
  const [pendingIntegrationsFocus, setPendingIntegrationsFocus] = useState(initialFocus === "integrations");
  const [advancedOpen, setAdvancedOpen] = useState(false);

  useEffect(() => {
    inputRef.current?.focus();
    const restore = restoreFocusRef.current;
    return () => {
      if (restore instanceof HTMLElement && restore.isConnected) restore.focus();
    };
  }, []);

  const browsing = query.trim() === "";
  const nodesQuery = useQuery({ queryKey: ["catalogNodes"], queryFn: ensureNodesCached, staleTime: Infinity });
  const search = useCatalogSearch({ query, kind, category, requireQuery: true, enabled: !browsing });
  const browse = useCatalogIntegrations({ kind, category, enabled: browsing });
  const expanded = useCatalogSearch({
    query: "",
    kind,
    integration: expandedIntegration ?? "",
    enabled: browsing && !!expandedIntegration,
  });
  // Typed, but the debounce has not settled: results for it are on their way.
  const searchPending = !browsing && (search.isLoading || query.trim() !== search.settledQuery);

  const builtins = useMemo<Array<BuiltinPaletteItem | BuiltinTriggerPaletteItem>>(() => {
    // A category narrows to integrations; built-ins have none.
    if (category) return [];
    const all = kind === "trigger" ? [...BUILTIN_TRIGGER_ITEMS] : builtinItemsFromNodes(nodesQuery.data ?? []);
    return all.filter((item) => matchesBuiltin(item, query));
  }, [kind, category, query, nodesQuery.data]);

  const builtinRows = useMemo<PaletteItem[]>(() => {
    // Searching lists every match flat; browsing folds the building blocks.
    if (!browsing) return builtins;
    const core = builtins.filter((item) => !(item.kind === "builtin" && item.advanced));
    const advanced = builtins.filter((item) => item.kind === "builtin" && item.advanced);
    if (advanced.length === 0) return core;
    return [...core, { kind: "advanced-toggle", expanded: advancedOpen, count: advanced.length }, ...(advancedOpen ? advanced : [])];
  }, [browsing, builtins, advancedOpen]);

  const integrationRows = useMemo<PaletteItem[]>(() => {
    if (!browsing) return search.entries.map((entry) => ({ kind: "catalog" as const, entry }));
    const rows: PaletteItem[] = [];
    for (const listing of browse.integrations) {
      const id = listing.integration.id;
      const isOpen = id === expandedIntegration;
      rows.push({ kind: "integration", listing, expanded: isOpen });
      if (!isOpen) continue;
      for (const entry of expanded.entries) rows.push({ kind: "catalog", entry, nested: true });
      if (expanded.hasNextPage) {
        rows.push({ kind: "more-entries", integrationId: id, remaining: Math.max(expanded.totalSize - expanded.entries.length, 0) });
      }
    }
    return rows;
  }, [browsing, search.entries, browse.integrations, expandedIntegration, expanded.entries, expanded.hasNextPage, expanded.totalSize]);

  const items = useMemo<PaletteItem[]>(() => [...builtinRows, ...integrationRows], [builtinRows, integrationRows]);

  // A new query or filter starts at the top.
  const changeQuery = (value: string) => {
    setQuery(value);
    setActiveKey(null);
  };
  const changeCategory = (value: string) => {
    setCategory(value);
    setActiveKey(null);
  };
  const changeKind = (value: CatalogKind) => {
    setKind(value);
    setCategory("");
    setExpandedIntegration(null);
    setActiveKey(null);
  };

  const firstIntegration = browse.integrations[0]?.integration.id;
  useEffect(() => {
    if (!pendingIntegrationsFocus || !firstIntegration) return;
    setPendingIntegrationsFocus(false);
    setActiveKey(`integration:${firstIntegration}`);
  }, [pendingIntegrationsFocus, firstIntegration]);

  const keyedIndex = activeKey ? items.findIndex((item) => paletteItemKey(item) === activeKey) : -1;
  const clampedIndex = items.length === 0 ? -1 : keyedIndex >= 0 ? keyedIndex : 0;
  const activeItem = clampedIndex >= 0 ? items[clampedIndex] : undefined;
  const activeEntry = activeItem?.kind === "catalog" ? activeItem.entry : undefined;
  const setActiveIndex = (index: number) => {
    const item = items[index];
    if (item) setActiveKey(paletteItemKey(item));
  };

  // Warm the full entry (schemas) of whatever the user is about to pick, so
  // choosing it inserts defaults and opens its form without a wait.
  useEffect(() => {
    if (!activeEntry) return;
    void queryClient.prefetchQuery({
      queryKey: catalogSearchKeys.entry(activeEntry.ref),
      queryFn: () => catalogSearchGrpc.get(activeEntry.ref),
      staleTime: 5 * 60_000,
    });
  }, [activeEntry, queryClient]);

  useEffect(() => {
    if (clampedIndex < 0) return;
    const element = document.getElementById(optionId(clampedIndex));
    if (element && typeof element.scrollIntoView === "function") element.scrollIntoView({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- optionId is derived from a stable id
  }, [clampedIndex]);

  // The end of the list is the end of whichever integration list is showing.
  const paged = browsing ? browse : search;
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = paged;
  const maybeFetchMore = useCallback(
    (index: number) => {
      if (hasNextPage && !isFetchingNextPage && index >= items.length - PREFETCH_THRESHOLD) fetchNextPage();
    },
    [hasNextPage, isFetchingNextPage, items.length, fetchNextPage],
  );

  // Scrolling (not just arrowing) to the end loads the next page too.
  const onListScroll = () => {
    const list = listRef.current;
    if (!list) return;
    if (list.scrollTop + list.clientHeight >= list.scrollHeight - 48) maybeFetchMore(items.length);
  };

  const toggleIntegration = (id: string) => setExpandedIntegration((current) => (current === id ? null : id));

  const choose = (item: PaletteItem | undefined) => {
    if (!item) return;
    switch (item.kind) {
      case "builtin":
        onChooseBuiltin(item.type);
        break;
      case "builtin-trigger":
        onChooseBuiltinTrigger?.(item.source);
        break;
      case "catalog":
        if (item.entry.kind === "trigger") onChooseTrigger?.(item.entry);
        else onChooseAction(item.entry);
        break;
      case "advanced-toggle":
        setAdvancedOpen((open) => !open);
        break;
      case "integration":
        toggleIntegration(item.listing.integration.id);
        break;
      case "more-entries":
        expanded.fetchNextPage();
        break;
    }
  };

  /** → expands and ← collapses while browsing; while typing they move the caret. */
  const expandOrCollapse = (direction: "open" | "close"): boolean => {
    if (!browsing || !activeItem) return false;
    if (activeItem.kind === "integration") {
      const id = activeItem.listing.integration.id;
      if (direction === "open" && !activeItem.expanded) setExpandedIntegration(id);
      else if (direction === "close" && activeItem.expanded) setExpandedIntegration(null);
      return true;
    }
    if (activeItem.kind === "advanced-toggle") {
      setAdvancedOpen(direction === "open");
      return true;
    }
    if (direction === "close" && ((activeItem.kind === "catalog" && activeItem.nested) || activeItem.kind === "more-entries")) {
      if (expandedIntegration) setActiveKey(`integration:${expandedIntegration}`);
      setExpandedIntegration(null);
      return true;
    }
    if (direction === "close" && activeItem.kind === "builtin" && activeItem.advanced) {
      setActiveKey("advanced-toggle");
      setAdvancedOpen(false);
      return true;
    }
    return false;
  };

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case "ArrowDown": {
        event.preventDefault();
        if (items.length === 0) return;
        const next = Math.min(clampedIndex + 1, items.length - 1);
        setActiveIndex(next);
        maybeFetchMore(next);
        return;
      }
      case "ArrowUp":
        event.preventDefault();
        setActiveIndex(Math.max(clampedIndex - 1, 0));
        return;
      case "ArrowRight":
        if (expandOrCollapse("open")) event.preventDefault();
        return;
      case "ArrowLeft":
        if (expandOrCollapse("close")) event.preventDefault();
        return;
      case "Home":
        if (!query) {
          event.preventDefault();
          setActiveIndex(0);
        }
        return;
      case "End":
        if (!query) {
          event.preventDefault();
          setActiveIndex(Math.max(items.length - 1, 0));
          maybeFetchMore(items.length - 1);
        }
        return;
      case "Enter":
        event.preventDefault();
        if (event.shiftKey && activeEntry && !activeEntry.connected) {
          onConnect(activeEntry);
          return;
        }
        choose(activeItem);
        return;
      case "Escape":
        event.preventDefault();
        event.stopPropagation();
        onClose();
        return;
    }
  };

  // The Connect pill inside a row is a pointer affordance only: an option's
  // children are presentational, so keyboard and AT users reach the same
  // action through ⇧↵ and the footer button.
  const onRowClick = (event: MouseEvent, item: PaletteItem) => {
    const wantsConnect = (event.target as HTMLElement).closest("[data-palette-connect]");
    if (wantsConnect && item.kind === "catalog") {
      onConnect(item.entry);
      return;
    }
    setActiveKey(paletteItemKey(item));
    choose(item);
  };

  const kindLabel = kind === "trigger" ? "triggers" : "steps";
  const facets = browsing ? browse.facets : search.facets;
  const showFacets = facets.length > 1 || !!category;
  const integrationsLoading = browsing ? browse.isLoading : searchPending && search.entries.length === 0;
  const integrationsError = browsing ? browse.isError : search.isError;
  const noResults = !integrationsLoading && !integrationsError && !searchPending && items.length === 0;
  const entryNoun = kind === "trigger" ? "trigger" : "action";

  let index = -1;
  const renderRow = (item: PaletteItem) => {
    index += 1;
    const rowIndex = index;
    const active = rowIndex === clampedIndex;
    const unconnected = item.kind === "catalog" && !item.entry.connected;
    const nested = (item.kind === "catalog" && item.nested) || item.kind === "more-entries" || (item.kind === "builtin" && item.advanced && browsing);
    const disclosure = item.kind === "integration" || item.kind === "advanced-toggle" ? item.expanded : undefined;
    return (
      <li
        key={paletteItemKey(item)}
        id={optionId(rowIndex)}
        role="option"
        aria-selected={active}
        aria-describedby={unconnected ? `${ids}-connect-hint` : undefined}
        data-palette-row={item.kind}
        onMouseMove={() => {
          if (!active) setActiveIndex(rowIndex);
        }}
        onClick={(event) => onRowClick(event, item)}
        className={cn(
          "flex cursor-pointer items-center gap-3 rounded-lg px-2.5 py-2",
          nested && "ml-[1.375rem] rounded-l-none border-l border-border/60 pl-[1.375rem]",
          active ? "bg-muted" : "hover:bg-muted/60",
        )}
      >
        <PaletteRowIcon item={item} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">{paletteItemLabel(item)}</span>
            {item.kind === "catalog" && !item.nested && (
              <span className="flex-shrink-0 text-xs text-muted-foreground">{item.entry.integration.displayName}</span>
            )}
            {disclosure !== undefined && <span className="sr-only">{disclosure ? ", expanded" : ", collapsed"}</span>}
          </div>
          <RowDescription item={item} entryNoun={entryNoun} />
        </div>
        {item.kind === "catalog" && item.entry.mutates && <Badge label="Changes data" variant="warning" size="sm" />}
        {item.kind === "integration" &&
          (item.listing.connected ? (
            <Badge label="Connected" variant="success" size="sm" dot />
          ) : (
            <span className="flex-shrink-0 text-2xs text-muted-foreground">Not connected</span>
          ))}
        {unconnected && (
          <span
            data-palette-connect
            className="flex-shrink-0 rounded-full border border-border px-2 py-0.5 text-2xs font-medium text-primary hover:bg-background"
          >
            Connect
          </span>
        )}
        {disclosure !== undefined ? (
          disclosure ? (
            <ChevronDown className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
          ) : (
            <ChevronRight className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
          )
        ) : (
          active && <CornerDownLeft className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
        )}
      </li>
    );
  };

  /** What sits under an expanded integration while its entries load or fail. */
  const renderExpandedStatus = (integrationId: string, integrationName: string): ReactElement | null => {
    if (!expanded.isLoading && !expanded.isError) return null;
    return (
      <li key={`status:${integrationId}`} role="presentation" className="ml-[1.375rem] border-l border-border/60 py-2 pl-[1.375rem]">
        {expanded.isLoading ? (
          <span role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
            Loading {integrationName}'s {entryNoun}s…
          </span>
        ) : (
          <span role="alert" className="flex items-center gap-3 text-xs">
            <span className="text-foreground">Couldn't load {integrationName}'s {entryNoun}s.</span>
            <button
              type="button"
              onClick={() => expanded.refetch()}
              className="font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              Retry
            </button>
          </span>
        )}
      </li>
    );
  };

  const renderIntegrationRows = () => {
    const out: ReactElement[] = [];
    for (const item of integrationRows) {
      out.push(renderRow(item));
      if (item.kind === "integration" && item.expanded) {
        const status = renderExpandedStatus(item.listing.integration.id, item.listing.integration.displayName);
        if (status) out.push(status);
      }
    }
    return out;
  };

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center px-4 pt-[12vh]" data-modal-open="true">
      <div className="absolute inset-0 bg-black/50" aria-hidden onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={kind === "trigger" ? "Add a trigger" : "Add a step"}
        className="relative flex max-h-[70vh] w-full max-w-2xl flex-col overflow-hidden rounded-xl border border-border bg-card shadow-2xl"
      >
        <div className="flex items-center gap-2 border-b border-border px-4 py-3">
          {search.isFetching && !search.isLoading ? (
            <Loader2 className="h-4 w-4 flex-shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden />
          ) : (
            <Search className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden />
          )}
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={clampedIndex >= 0 ? optionId(clampedIndex) : undefined}
            aria-label={`Search ${kindLabel}`}
            value={query}
            onChange={(event) => changeQuery(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder={kind === "trigger" ? "Search triggers: “new issue”, “schedule”…" : "Search steps and integrations: “create issue”, “slack”…"}
            className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
          />
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {(allowKindSwitch || showFacets) && (
          <div className="flex flex-wrap items-center gap-1.5 border-b border-border/60 px-4 py-2">
            {allowKindSwitch && (
              <div role="group" aria-label="What to add" className="mr-2 flex rounded-lg border border-border/60 bg-background p-0.5">
                {(["action", "trigger"] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    aria-pressed={kind === value}
                    onClick={() => {
                      changeKind(value);
                      inputRef.current?.focus();
                    }}
                    className={cn(
                      "rounded-md px-2.5 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      kind === value ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {value === "action" ? "Steps" : "Triggers"}
                  </button>
                ))}
              </div>
            )}
            {showFacets && (
              <div role="group" aria-label="Category" className="flex flex-wrap gap-1.5">
                <FacetChip label="All" pressed={!category} onClick={() => changeCategory("")} />
                {facets.map((facet) => (
                  <FacetChip
                    key={facet.value}
                    label={categoryLabel(facet.value)}
                    count={facet.count}
                    pressed={category === facet.value}
                    onClick={() => changeCategory(category === facet.value ? "" : facet.value)}
                  />
                ))}
              </div>
            )}
          </div>
        )}

        <ul
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label={kind === "trigger" ? "Triggers" : "Steps"}
          onScroll={onListScroll}
          className="flex-1 overflow-y-auto p-2"
        >
          {builtinRows.length > 0 && (
            <li role="presentation">
              <div className="px-2.5 pb-1 pt-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground" aria-hidden>
                Built-in
              </div>
              <ul role="group" aria-label="Built-in" className="space-y-0.5">
                {builtinRows.map(renderRow)}
              </ul>
            </li>
          )}

          {(integrationRows.length > 0 || integrationsLoading || integrationsError) && (
            <li role="presentation" className={cn(builtinRows.length > 0 && "mt-2")}>
              <div className="flex items-baseline justify-between px-2.5 pb-1 pt-1.5" aria-hidden>
                <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Integrations</span>
                {browsing
                  ? browse.totalSize > 0 && <span className="text-2xs text-muted-foreground">{browse.totalSize}</span>
                  : search.totalSize > 0 && (
                      <span className="text-2xs text-muted-foreground">
                        {search.entries.length} of {search.totalSize}
                      </span>
                    )}
              </div>
              {integrationsLoading ? (
                <div role="status" aria-label={browsing ? "Loading integrations" : "Searching integrations"} className="space-y-1 px-2.5 py-1">
                  {[0, 1, 2].map((i) => (
                    <div key={i} className="flex items-center gap-3 py-1.5">
                      <div className="h-8 w-8 animate-pulse rounded-lg bg-muted motion-reduce:animate-none" />
                      <div className="flex-1 space-y-1.5">
                        <div className="h-3 w-1/3 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                        <div className="h-2.5 w-2/3 animate-pulse rounded bg-muted motion-reduce:animate-none" />
                      </div>
                    </div>
                  ))}
                </div>
              ) : integrationsError ? (
                <div role="alert" className="flex items-center justify-between gap-3 rounded-lg border border-border/60 bg-background px-3 py-2.5 text-sm">
                  <span className="text-foreground">{browsing ? "Couldn't load integrations." : "Couldn't search integrations."}</span>
                  <button
                    type="button"
                    onClick={() => paged.refetch()}
                    className="font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    Retry
                  </button>
                </div>
              ) : (
                <ul role="group" aria-label="Integrations" className="space-y-0.5">
                  {renderIntegrationRows()}
                </ul>
              )}
              {paged.isFetchingNextPage && (
                <div role="status" className="flex items-center gap-2 px-2.5 py-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
                  Loading more…
                </div>
              )}
              {browsing && !browse.isLoading && !browse.isError && !browse.hasNextPage && browse.totalSize > 0 && (
                <p className="px-2.5 pb-1 pt-2 text-2xs text-muted-foreground">
                  Type to search every {entryNoun} across {browse.totalSize === 1 ? "1 integration" : `${browse.totalSize} integrations`}.
                </p>
              )}
            </li>
          )}

          {noResults && (
            <li role="presentation" className="px-4 py-10 text-center">
              <p className="text-sm font-medium text-foreground">
                {search.settledQuery && !browsing ? `No ${kindLabel} match “${search.settledQuery}”` : `No ${kindLabel} available`}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {category ? "Try another category, or search all of them." : "Try a shorter or different word."}
              </p>
              {category && (
                <button
                  type="button"
                  onClick={() => changeCategory("")}
                  className="mt-3 text-xs font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  Search all categories
                </button>
              )}
            </li>
          )}
        </ul>

        <div className="flex items-center justify-between gap-3 border-t border-border/60 px-4 py-2 text-2xs text-muted-foreground">
          {activeEntry && !activeEntry.connected ? (
            <span id={`${ids}-connect-hint`} className="flex items-center gap-2">
              <span>{activeEntry.integration.displayName} isn't connected.</span>
              <button
                type="button"
                onClick={() => onConnect(activeEntry)}
                className="font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                Connect {activeEntry.integration.displayName}
              </button>
              <Kbd>⇧↵</Kbd>
            </span>
          ) : (
            <span id={`${ids}-connect-hint`} className="sr-only">
              Press Shift Enter to connect.
            </span>
          )}
          <span className="ml-auto flex items-center gap-3" aria-hidden>
            <span><Kbd>↑↓</Kbd> move</span>
            {browsing && <span><Kbd>→</Kbd> expand</span>}
            <span><Kbd>↵</Kbd> add</span>
            <span><Kbd>esc</Kbd> close</span>
          </span>
        </div>
      </div>
    </div>
  );
}

function RowDescription({ item, entryNoun }: { item: PaletteItem; entryNoun: "action" | "trigger" }) {
  let text: string;
  switch (item.kind) {
    case "catalog":
      text = item.entry.summary;
      break;
    case "integration":
      text = [entryCountLabel(item.listing.entryCount, entryNoun), item.listing.integration.category && categoryLabel(item.listing.integration.category)]
        .filter(Boolean)
        .join(" · ");
      break;
    case "advanced-toggle":
      text = "The steps an Agent runs for you, for building your own loop";
      break;
    case "more-entries":
      text = `Load the rest of this integration's ${entryNoun}s`;
      break;
    default:
      text = item.description;
  }
  return <p className="truncate text-xs text-muted-foreground">{text}</p>;
}

function PaletteRowIcon({ item }: { item: PaletteItem }) {
  switch (item.kind) {
    case "catalog":
      // An entry under its expanded integration already sits beneath that
      // integration's logo; repeating it on every row is noise.
      if (item.nested) return null;
      return <IntegrationLogoTile icon={item.entry.integration.icon} />;
    case "integration":
      return <IntegrationLogoTile icon={item.listing.integration.icon || item.listing.integration.id} />;
    case "more-entries":
      return null;
    case "advanced-toggle":
      return (
        <span aria-hidden className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg border border-border/60 bg-background">
          <Blocks className="h-4 w-4 text-muted-foreground" />
        </span>
      );
    default: {
      const iconKey = item.kind === "builtin" ? item.type : item.source === "schedule" ? "clock" : item.source === "webhook" ? "link" : "workflow";
      const Icon = getNodeIcon(iconKey);
      return (
        <span aria-hidden className={cn("flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg", getNodeBgColor(iconKey))}>
          <Icon className="h-4 w-4 text-white" />
        </span>
      );
    }
  }
}

function FacetChip({ label, count, pressed, onClick }: { label: string; count?: number; pressed: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        "rounded-full border px-2.5 py-0.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        pressed ? "border-primary bg-primary text-primary-foreground" : "border-border text-muted-foreground hover:bg-muted hover:text-foreground",
      )}
    >
      {label}
      {count !== undefined && <span className={cn("ml-1", pressed ? "opacity-80" : "opacity-70")}>{count}</span>}
    </button>
  );
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="rounded border border-border/60 bg-background px-1 py-px font-mono text-2xs text-muted-foreground">{children}</kbd>
  );
}
