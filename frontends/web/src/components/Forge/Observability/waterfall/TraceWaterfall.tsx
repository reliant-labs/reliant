/**
 * The trace waterfall: one row per span, indented by depth, with a bar placed
 * on the trace's timeline.
 *
 * Ported from HyperDX's DBTraceWaterfallChart.tsx
 * (https://github.com/hyperdxio/hyperdx, packages/app/src/components/
 * DBTraceWaterfallChart.tsx). The layout — a name column with collapse
 * chevrons and child counts beside a timeline column, error spans always red,
 * every other span in a stable per-service colour, span events as markers,
 * the depth controls and the "N spans · M errors · total" summary — is
 * HyperDX's. The Mantine components and SCSS are replaced with Reliant's
 * semantic tokens, and the rows follow the WAI-ARIA tree pattern so the
 * waterfall is keyboard-operable. Attribution is also recorded in
 * LICENSES.txt at the repository root.
 *
 * ---------------------------------------------------------------------------
 * MIT License
 *
 * Copyright (c) 2023 DeploySentinel, Inc.
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 * ---------------------------------------------------------------------------
 */

import { useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ChevronDown, ChevronRight, ChevronsDown, ChevronsRight } from "lucide-react";

import { Button } from "@/components/ui/Button";
import type { Span } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";

import { formatDuration } from "../format";
import {
  buildTraceTree,
  computeCollapseAll,
  computeCollapseOneLevel,
  computeExpandOneLevel,
  computeToggleCollapse,
  flattenTree,
  serviceSlots,
  traceExtent,
} from "./traceTree";
import { useRovingFocus } from "../useRovingFocus";

const ROW_HEIGHT = 28;
/**
 * A phone has no room for the name column beside the timeline (12rem of a
 * 390px screen left the bars ~140px), so each span is two lines there: its
 * name, then its bar across the full width. 52px also makes the row a
 * comfortable tap target.
 */
const NARROW_ROW_HEIGHT = 52;
const INDENT_REM = 0.875;
/** Name column beside the timeline, or the name above it on a phone. */
const COLUMNS_WIDE = "grid-cols-[minmax(12rem,2fr)_3fr]";
const COLUMNS_NARROW = "grid-cols-1 grid-rows-[1.75rem_1fr]";

/**
 * Service colours, as theme tokens. Red (`--destructive`) is NOT here: it
 * means "this span failed" and nothing else, as in HyperDX.
 */
const SERVICE_TOKENS = ["--primary", "--secondary", "--success", "--warning", "--muted-foreground", "--accent-foreground"];

function serviceColor(slot: number | undefined): string {
  return `hsl(var(${SERVICE_TOKENS[(slot ?? 0) % SERVICE_TOKENS.length]}))`;
}

export function TraceWaterfall({
  spans,
  truncated,
  selectedSpanId,
  onSelect,
}: {
  spans: Span[];
  truncated: boolean;
  selectedSpanId: string | undefined;
  onSelect: (spanId: string) => void;
}) {
  const tree = useMemo(() => buildTraceTree(spans), [spans]);
  const extent = useMemo(() => traceExtent(spans), [spans]);
  const slots = useMemo(() => serviceSlots(spans), [spans]);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const flat = useMemo(() => flattenTree(tree.roots, collapsed), [tree, collapsed]);
  const parentOf = useMemo(() => {
    const map = new Map<string, string>();
    tree.nodesById.forEach((node) => node.children.forEach((child) => map.set(child.id, node.id)));
    return map;
  }, [tree]);

  const selectedIndex = flat.findIndex((row) => row.node.span.spanId === selectedSpanId);
  // The roving tab stop. Until the reader moves it, it sits on the selected
  // span (a deep link to a span lands keyboard focus there), else the root.
  const [active, setActive] = useState(-1);
  const activeIndex = Math.max(0, Math.min(active >= 0 ? active : Math.max(selectedIndex, 0), flat.length - 1));

  const narrow = useNarrowViewport();
  const rowHeight = narrow ? NARROW_ROW_HEIGHT : ROW_HEIGHT;
  const columns = narrow ? COLUMNS_NARROW : COLUMNS_WIDE;

  const scrollRef = useRef<HTMLDivElement>(null);
  const requestFocus = useRovingFocus(scrollRef, "data-span-index");
  const virtualizer = useVirtualizer({
    count: flat.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    getItemKey: (index) => flat[index]?.node.id ?? index,
    overscan: 20,
  });

  const errorCount = spans.filter((span) => span.status === "error").length;
  const total = extent?.totalDurationMs ?? 0;
  const origin = extent?.startMs ?? 0;

  const toggle = (id: string, event?: MouseEvent) => {
    event?.stopPropagation();
    setCollapsed((prev) => computeToggleCollapse(prev, id, tree.nodesById.get(id), !!event?.altKey));
  };

  const focusRow = (index: number) => {
    const clamped = Math.max(0, Math.min(flat.length - 1, index));
    setActive(clamped);
    virtualizer.scrollToIndex(clamped, { align: "auto" });
    requestFocus(clamped);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const row = flat[activeIndex];
    if (!row) return;
    const { node } = row;
    const isOpen = node.children.length > 0 && !collapsed.has(node.id);
    switch (event.key) {
      case "ArrowDown":
        focusRow(activeIndex + 1);
        break;
      case "ArrowUp":
        focusRow(activeIndex - 1);
        break;
      case "Home":
        focusRow(0);
        break;
      case "End":
        focusRow(flat.length - 1);
        break;
      case "ArrowRight":
        if (node.children.length === 0) return;
        if (!isOpen) toggle(node.id);
        else focusRow(activeIndex + 1);
        break;
      case "ArrowLeft": {
        if (isOpen) {
          toggle(node.id);
          break;
        }
        const parentId = parentOf.get(node.id);
        const parentIndex = parentId ? flat.findIndex((candidate) => candidate.node.id === parentId) : -1;
        if (parentIndex < 0) return;
        focusRow(parentIndex);
        break;
      }
      case "Enter":
      case " ":
        onSelect(node.span.spanId);
        break;
      default:
        return;
    }
    event.preventDefault();
  };

  const hasCollapsible = tree.parentIdsByLevel.size > 0;
  const ticks = [0, 0.25, 0.5, 0.75, 1];

  return (
    <div className="space-y-2" data-testid="trace-waterfall">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground" data-testid="trace-waterfall-summary">
          {spans.length} span{spans.length === 1 ? "" : "s"} ·{" "}
          <span className={errorCount > 0 ? "text-destructive" : undefined}>
            {errorCount} error{errorCount === 1 ? "" : "s"}
          </span>{" "}
          · Total {formatDuration(total)}
          {truncated || extent?.startClipped ? "+" : ""}
          {truncated && <span> — the server returned the first spans only; the trace is longer than shown.</span>}
        </p>
        {hasCollapsible && (
          <div className="flex flex-wrap gap-1 [&>button]:touch:min-h-[44px]" role="group" aria-label="Expand and collapse spans">
            <Button variant="ghost" size="xs" onClick={() => setCollapsed(new Set())} leftIcon={<ChevronsDown className="h-3 w-3" />}>
              Expand all
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setCollapsed((prev) => computeExpandOneLevel(prev, tree.parentIdsByLevel))}
            >
              Expand one level
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setCollapsed((prev) => computeCollapseOneLevel(prev, tree.parentIdsByLevel))}
            >
              Collapse one level
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setCollapsed(computeCollapseAll(tree.parentIdsByLevel))}
              leftIcon={<ChevronsRight className="h-3 w-3" />}
            >
              Collapse all
            </Button>
          </div>
        )}
      </div>

      <div className="overflow-hidden rounded-md border border-border/60 bg-background">
        {/* The time ruler. Decorative: every bar's row says its own offset and duration. */}
        <div aria-hidden="true" className={cn("grid border-b border-border/60 text-2xs text-muted-foreground", narrow ? "grid-cols-1 px-3" : COLUMNS_WIDE)}>
          {!narrow && <div className="px-3 py-1">Span</div>}
          <div className="relative h-6">
            {ticks.map((tick) => (
              <span
                key={tick}
                className="absolute top-1 -translate-x-1/2 tabular-nums first:translate-x-0 last:-translate-x-full"
                style={{ left: `${tick * 100}%` }}
              >
                {formatDuration(total * tick)}
              </span>
            ))}
          </div>
        </div>
        <div ref={scrollRef} className="max-h-[55vh] overflow-auto" onKeyDown={onKeyDown}>
          <div role="tree" aria-label="Spans of this trace" className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((item) => {
              const row = flat[item.index];
              if (!row) return null;
              const { node, level } = row;
              const { span } = node;
              const offset = span.startMs - origin;
              const left = total > 0 ? (offset / total) * 100 : 0;
              const width = total > 0 ? Math.max((span.durationMs / total) * 100, 0.25) : 100;
              const isError = span.status === "error";
              const isSelected = span.spanId === selectedSpanId;
              const hasChildren = node.children.length > 0;
              const isOpen = hasChildren && !collapsed.has(node.id);
              const color = isError ? "hsl(var(--destructive))" : serviceColor(slots.get(span.service));
              return (
                <div
                  key={item.key}
                  role="treeitem"
                  aria-level={level + 1}
                  aria-expanded={hasChildren ? isOpen : undefined}
                  aria-selected={isSelected}
                  aria-label={`${span.service} ${span.name}, starts at ${formatDuration(offset)}, takes ${formatDuration(span.durationMs)}${isError ? ", failed" : ""}`}
                  tabIndex={item.index === activeIndex ? 0 : -1}
                  data-span-index={item.index}
                  data-testid="trace-span-row"
                  onClick={() => {
                    setActive(item.index);
                    onSelect(span.spanId);
                  }}
                  onFocus={() => setActive(item.index)}
                  className={cn(
                    "absolute left-0 top-0 grid w-full cursor-pointer items-center text-xs hover:bg-muted/50 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                    columns,
                    narrow && "border-b border-border/40",
                    isSelected && "bg-muted/60"
                  )}
                  style={{ height: rowHeight, transform: `translateY(${item.start}px)` }}
                >
                  <div className="flex min-w-0 items-center gap-1 pr-2" style={{ paddingLeft: `${0.5 + level * INDENT_REM}rem` }}>
                    {hasChildren ? (
                      <button
                        type="button"
                        tabIndex={-1}
                        aria-label={isOpen ? "Collapse" : "Expand"}
                        title="Alt-click to toggle the whole subtree"
                        onClick={(event) => toggle(node.id, event)}
                        className="shrink-0 rounded-sm text-muted-foreground hover:text-foreground touch-target touch:-my-2 touch:-ml-3 inline-flex items-center justify-center"
                      >
                        {isOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
                      </button>
                    ) : (
                      <span className="w-3 shrink-0" />
                    )}
                    <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: color }} aria-hidden="true" />
                    <span className="shrink-0 text-muted-foreground">{span.service}</span>
                    <span className={cn("truncate font-mono", isError ? "text-destructive" : "text-foreground")}>{span.name}</span>
                    {hasChildren && !isOpen && (
                      <span className="shrink-0 text-muted-foreground">({node.children.length})</span>
                    )}
                  </div>
                  <div className={cn("relative h-full", narrow && "mx-3")} aria-hidden="true">
                    <div
                      className="absolute top-1/2 h-3 -translate-y-1/2 rounded-sm"
                      style={{ left: `${left}%`, width: `${width}%`, backgroundColor: color, opacity: isSelected ? 1 : 0.85 }}
                    />
                    {span.events.map((event, index) => {
                      const at = total > 0 ? ((event.timeMs - origin) / total) * 100 : 0;
                      return (
                        <span
                          key={`${event.name}-${index}`}
                          title={event.name}
                          className="absolute top-1/2 h-1.5 w-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-foreground"
                          style={{ left: `${at}%` }}
                        />
                      );
                    })}
                    <span
                      className="absolute top-1/2 -translate-y-1/2 whitespace-nowrap px-1 text-2xs tabular-nums text-muted-foreground"
                      style={left + width > 80 ? { right: `${100 - left}%` } : { left: `${left + width}%` }}
                    >
                      {formatDuration(span.durationMs)}
                    </span>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}
