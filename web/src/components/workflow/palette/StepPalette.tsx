// Copyright (c) 2025 Reliant Labs

/**
 * StepPalette — the one place to add anything to a workflow
 * (research/INTEGRATIONS_V1_BRIEF.md §3a): core node types as a "Built-in"
 * group, and integration actions and triggers from SearchCatalog.
 *
 * Built for a catalog of hundreds: the server does the matching, results are
 * paged by token as the user scrolls or arrows down, and nothing fetches the
 * whole catalog. Keyboard-first as an ARIA 1.2 combobox: the input keeps
 * focus, ↑/↓ move the active option, ↵ adds it, ⇧↵ connects an integration
 * that is not connected, Esc closes.
 */

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CornerDownLeft, Loader2, Search, X } from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { createPortal } from "react-dom";

import Badge from "../../forge-ui/badge";
import { catalogSearchGrpc, type CatalogEntrySummary, type CatalogKind } from "../../../api/catalog-search-grpc";
import { ensureNodesCached, getNodeBgColor, getNodeIcon } from "../../../lib/node-metadata";
import { cn } from "../../../lib/utils";
import { IntegrationIcon } from "./IntegrationIcon";
import {
  BUILTIN_TRIGGER_ITEMS,
  builtinItemsFromNodes,
  matchesBuiltin,
  paletteItemKey,
  paletteItemLabel,
  type BuiltinPaletteItem,
  type BuiltinTriggerPaletteItem,
  type PaletteItem,
} from "./paletteItems";
import { catalogSearchKeys, useCatalogSearch } from "./useCatalogSearch";

export interface StepPaletteProps {
  open: boolean;
  onClose: () => void;
  /** Which kind the palette opens on. */
  initialKind?: CatalogKind;
  /** Offer the Steps / Triggers switch. Off where only one kind can be added. */
  allowKindSwitch?: boolean;
  onChooseBuiltin: (type: string) => void;
  onChooseAction: (entry: CatalogEntrySummary) => void;
  onChooseTrigger?: (entry: CatalogEntrySummary) => void;
  onChooseBuiltinTrigger?: (source: BuiltinTriggerPaletteItem["source"]) => void;
  /** Start connecting the entry's integration. */
  onConnect: (entry: CatalogEntrySummary) => void;
}

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
  const [activeIndex, setActiveIndex] = useState(0);

  useEffect(() => {
    inputRef.current?.focus();
    const restore = restoreFocusRef.current;
    return () => {
      if (restore instanceof HTMLElement && restore.isConnected) restore.focus();
    };
  }, []);

  const nodesQuery = useQuery({ queryKey: ["catalogNodes"], queryFn: ensureNodesCached, staleTime: Infinity });
  const search = useCatalogSearch({ query, kind, category });

  const builtins = useMemo<Array<BuiltinPaletteItem | BuiltinTriggerPaletteItem>>(() => {
    // A category narrows to integrations; built-ins have none.
    if (category) return [];
    const all = kind === "trigger" ? [...BUILTIN_TRIGGER_ITEMS] : builtinItemsFromNodes(nodesQuery.data ?? []);
    return all.filter((item) => matchesBuiltin(item, query));
  }, [kind, category, query, nodesQuery.data]);

  const items = useMemo<PaletteItem[]>(
    () => [...builtins, ...search.entries.map((entry) => ({ kind: "catalog" as const, entry }))],
    [builtins, search.entries],
  );

  // A new query or filter starts at the top.
  useEffect(() => {
    setActiveIndex(0);
  }, [query, kind, category]);

  const clampedIndex = items.length === 0 ? -1 : Math.min(activeIndex, items.length - 1);
  const activeItem = clampedIndex >= 0 ? items[clampedIndex] : undefined;
  const activeEntry = activeItem?.kind === "catalog" ? activeItem.entry : undefined;

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

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = search;
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
    }
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
    choose(item);
  };

  const kindLabel = kind === "trigger" ? "triggers" : "steps";
  const showFacets = search.facets.length > 1 || !!category;
  const noResults = !search.isLoading && !search.isError && items.length === 0;

  let index = -1;
  const renderRow = (item: PaletteItem) => {
    index += 1;
    const rowIndex = index;
    const active = rowIndex === clampedIndex;
    const unconnected = item.kind === "catalog" && !item.entry.connected;
    return (
      <li
        key={paletteItemKey(item)}
        id={optionId(rowIndex)}
        role="option"
        aria-selected={active}
        aria-describedby={unconnected ? `${ids}-connect-hint` : undefined}
        onMouseMove={() => {
          if (!active) setActiveIndex(rowIndex);
        }}
        onClick={(event) => onRowClick(event, item)}
        className={cn(
          "flex cursor-pointer items-center gap-3 rounded-lg px-2.5 py-2",
          active ? "bg-muted" : "hover:bg-muted/60",
        )}
      >
        <PaletteRowIcon item={item} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">{paletteItemLabel(item)}</span>
            {item.kind === "catalog" && (
              <span className="flex-shrink-0 text-xs text-muted-foreground">{item.entry.integration.displayName}</span>
            )}
          </div>
          <p className="truncate text-xs text-muted-foreground">
            {item.kind === "catalog" ? item.entry.summary : item.description}
          </p>
        </div>
        {item.kind === "catalog" && item.entry.mutates && <Badge label="Changes data" variant="warning" size="sm" />}
        {unconnected && (
          <span
            data-palette-connect
            className="flex-shrink-0 rounded-full border border-border px-2 py-0.5 text-2xs font-medium text-primary hover:bg-background"
          >
            Connect
          </span>
        )}
        {active && <CornerDownLeft className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />}
      </li>
    );
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
            onChange={(event) => setQuery(event.target.value)}
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
                      setKind(value);
                      setCategory("");
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
                <FacetChip label="All" pressed={!category} onClick={() => setCategory("")} />
                {search.facets.map((facet) => (
                  <FacetChip
                    key={facet.value}
                    label={categoryLabel(facet.value)}
                    count={facet.count}
                    pressed={category === facet.value}
                    onClick={() => setCategory(category === facet.value ? "" : facet.value)}
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
          {builtins.length > 0 && (
            <li role="presentation">
              <div className="px-2.5 pb-1 pt-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground" aria-hidden>
                Built-in
              </div>
              <ul role="group" aria-label="Built-in" className="space-y-0.5">
                {builtins.map(renderRow)}
              </ul>
            </li>
          )}

          {(search.entries.length > 0 || search.isLoading || search.isError) && (
            <li role="presentation" className={cn(builtins.length > 0 && "mt-2")}>
              <div className="flex items-baseline justify-between px-2.5 pb-1 pt-1.5" aria-hidden>
                <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Integrations</span>
                {search.totalSize > 0 && (
                  <span className="text-2xs text-muted-foreground">
                    {search.entries.length} of {search.totalSize}
                  </span>
                )}
              </div>
              {search.isLoading ? (
                <div role="status" aria-label="Searching integrations" className="space-y-1 px-2.5 py-1">
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
              ) : search.isError ? (
                <div role="alert" className="flex items-center justify-between gap-3 rounded-lg border border-border/60 bg-background px-3 py-2.5 text-sm">
                  <span className="text-foreground">Couldn't search integrations.</span>
                  <button
                    type="button"
                    onClick={() => search.refetch()}
                    className="font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    Retry
                  </button>
                </div>
              ) : (
                <ul role="group" aria-label="Integrations" className="space-y-0.5">
                  {search.entries.map((entry) => renderRow({ kind: "catalog", entry }))}
                </ul>
              )}
              {search.isFetchingNextPage && (
                <div role="status" className="flex items-center gap-2 px-2.5 py-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden />
                  Loading more…
                </div>
              )}
            </li>
          )}

          {noResults && (
            <li role="presentation" className="px-4 py-10 text-center">
              <p className="text-sm font-medium text-foreground">
                {search.settledQuery ? `No ${kindLabel} match “${search.settledQuery}”` : `No ${kindLabel} available`}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {category ? "Try another category, or search all of them." : "Try a shorter or different word."}
              </p>
              {category && (
                <button
                  type="button"
                  onClick={() => setCategory("")}
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
            <span><Kbd>↵</Kbd> add</span>
            <span><Kbd>esc</Kbd> close</span>
          </span>
        </div>
      </div>
    </div>
  );
}

function PaletteRowIcon({ item }: { item: PaletteItem }) {
  if (item.kind === "catalog") return <IntegrationIcon hint={item.entry.integration.icon} />;
  const iconKey = item.kind === "builtin" ? item.type : item.source === "schedule" ? "clock" : item.source === "webhook" ? "link" : "workflow";
  const Icon = getNodeIcon(iconKey);
  return (
    <span aria-hidden className={cn("flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg", getNodeBgColor(iconKey))}>
      <Icon className="h-4 w-4 text-white" />
    </span>
  );
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
