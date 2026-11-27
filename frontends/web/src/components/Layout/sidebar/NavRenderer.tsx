// Copyright (c) 2026 Reliant Labs

/**
 * THE ONE RENDERER for the nav config (navConfig/hybrid.ts, U6d prototype):
 * rows, accordions, headings, slots, and drill-in areas.
 *
 * DRILL-IN. The route decides it (navConfig/model.ts activeArea): on a route
 * an area claims, the nav's rows are REPLACED by the area's, under a
 * "‹ Area" header that links back to the project's Overview. Nothing about it
 * is local state, so Back, refresh and a shared link land on the same nav.
 *
 * ACCORDIONS. Open or closed is local, but the group holding the page on
 * screen opens on every navigation, and clicking an open group closes it —
 * the current one too (owner bug 3).
 *
 * FLAT (a phone's drawer; owner, 2026-11-24). In a drawer a drill-in is a new
 * screen, not a nesting, so `flat` draws every row like a top-level one: the
 * same size, weight, colour and indent, and an icon (`icon`, else
 * `flatIcon`). The nested nav keeps its hierarchy: deeper rows step in, dim,
 * and go bare.
 */

import { useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

import { externalMark } from "./navConfig/items";
import { activeArea, backHref, childrenOf, hrefOf, isCurrent, visibleItems } from "./navConfig/model";
import type { NavArea, NavBadge, NavConfig, NavCtx, NavGroup, NavItem, NavRow, NavSlotId } from "./navConfig/types";
import { SidebarFooter, SidebarRow, SidebarSubLabel, SidebarSubRow } from "./primitives";

/**
 * Where a slot is drawn: `fill` takes the rest of a drill-in's column (the
 * chat list, full height); `capped` sits in an accordion and scrolls inside
 * itself; `row` is a row's trail or control.
 */
export type SlotPlace = "fill" | "capped" | "row";
export interface SlotLayout {
  place: SlotPlace;
  depth: 0 | 1 | 2 | 3;
}
export type NavSlots = Partial<Record<NavSlotId, (layout: SlotLayout) => ReactNode>>;

export interface NavRendererProps {
  config: NavConfig;
  ctx: NavCtx;
  slots: NavSlots;
  /**
   * Runs a row's page. `drill`: the row opens a drill-in (or leaves one, the
   * back header), so a drawer holding the nav stays open to show it; any
   * other row is a destination, and a drawer closes on it.
   */
  onNavigate: (href: string, options?: NavigateOptions) => void;
  /** Every row drawn as a top-level row, icon included (a phone's drawer). */
  flat?: boolean;
  /**
   * Leaves a drill-in in place: the back header calls this instead of
   * navigating to backHref. A phone's drawer shows its top level this way,
   * because backHref is a desktop page (reliant #3264).
   */
  onBack?: () => void;
  /** Draws the top level even on a route an area claims (after `onBack`). */
  topLevel?: boolean;
}

export interface NavigateOptions {
  drill?: boolean;
}

type Depth = 0 | 1 | 2 | 3;
const deeper = (depth: Depth): Depth => Math.min(depth + 1, 3) as Depth;
const testIdOf = (item: NavItem) => item.testId ?? `nav-${item.id}`;

function allGroups(items: readonly NavItem[], ctx: NavCtx): NavItem[] {
  return visibleItems(items, ctx).flatMap((item) => (item.kind === "group" || item.kind === "area" ? [item, ...allGroups(childrenOf(item, ctx), ctx)] : []));
}

function Badge({ badge }: { badge: NavBadge }) {
  if (badge.type === "dot") return <span role="img" aria-label={badge.label} className="size-2 rounded-full bg-warning" />;
  if (badge.type === "label") {
    // A stage, not a status: a quiet outline, never a fill or a status hue.
    return <span className="rounded-md border border-border px-1.5 text-meta leading-4 text-muted-foreground">{badge.text}</span>;
  }
  return (
    <span className={cn("tabular-nums", badge.tone === "danger" && "font-medium text-destructive-ink")}>
      {badge.value}
      {badge.label && <span className="sr-only"> {badge.label}</span>}
    </span>
  );
}

export function NavRenderer({ config, ctx, slots, onNavigate, flat = false, onBack, topLevel = false }: NavRendererProps) {
  const area = topLevel ? null : activeArea(config, ctx);
  const link = (href: string) => href;
  // Where a row at `depth` is drawn, and the icon it shows there.
  const rowDepth = (depth: Depth): Depth => (flat ? 0 : depth);
  const rowIcon = (item: NavRow | NavGroup | NavArea, depth: Depth) => (flat ? (item.icon ?? item.flatIcon) : depth === 0 ? item.icon : undefined);

  // Accordions: the person's toggles, until a navigation reopens the group holding the new page.
  const [accordion, setAccordion] = useState<{ path: string; toggled: Record<string, boolean> }>({ path: ctx.pathname, toggled: {} });
  let toggled = accordion.toggled;
  if (accordion.path !== ctx.pathname) {
    const next = { ...accordion.toggled };
    for (const group of allGroups([...config.items, ...config.footer], ctx)) {
      if (group.kind === "group" && isCurrent(group, ctx)) delete next[group.id];
    }
    toggled = next;
    setAccordion({ path: ctx.pathname, toggled: next });
  }
  const isOpen = (id: string, current: boolean) => toggled[id] ?? current;
  const toggle = (id: string, open: boolean) => setAccordion((state) => ({ ...state, toggled: { ...state.toggled, [id]: !open } }));

  const slot = (id: NavSlotId | undefined, layout: SlotLayout) => (id ? slots[id]?.(layout) : undefined);

  function renderItems(items: readonly NavItem[], depth: Depth, where: "nav" | "footer" | "drill"): ReactNode[] {
    const out: ReactNode[] = [];
    for (const item of visibleItems(items, ctx)) {
      const rendered = renderItem(item, depth, where);
      // A tour anchor (NavItem `tour`): a group carries it on its own block; anything else in a box of its own.
      out.push(
        item.tour && item.kind !== "group" ? (
          <div key={item.id} data-tour={item.tour}>
            {rendered}
          </div>
        ) : (
          rendered
        ),
      );
    }
    return out;
  }

  function renderItem(item: NavItem, depth: Depth, where: "nav" | "footer" | "drill"): ReactNode {
    const testId = testIdOf(item);
    switch (item.kind) {
      case "heading":
        return (
          <SidebarSubLabel key={item.id} depth={Math.min(rowDepth(depth), 2) as 0 | 1 | 2}>
            {item.label}
          </SidebarSubLabel>
        );
      case "slot": {
        const place: SlotPlace = where === "drill" && item.slot === "chat-list" ? "fill" : "capped";
        return <SlotFrame key={item.id} id={item.id}>{slot(item.slot, { place, depth: rowDepth(depth) })}</SlotFrame>;
      }
      case "row": {
        const href = hrefOf(item, ctx);
        const current = isCurrent(item, ctx);
        const badge = item.badge?.(ctx);
        const trail =
          badge || item.trail || item.external ? (
            <>
              {slot(item.trail, { place: "row", depth: rowDepth(depth) })}
              {badge && <Badge badge={badge} />}
              {item.external && externalMark}
            </>
          ) : undefined;
        if (where === "footer") {
          return (
            <SidebarRow
              key={item.id}
              icon={item.icon}
              label={item.label}
              href={href ? link(href) : undefined}
              onClick={href ? () => onNavigate(link(href)) : undefined}
              current={current}
              trail={trail}
              action={slot(item.action, { place: "row", depth: rowDepth(depth) })}
              testId={testId}
            />
          );
        }
        return (
          <SidebarSubRow
            key={item.id}
            label={item.label}
            depth={rowDepth(depth)}
            icon={rowIcon(item, depth)}
            href={href ? link(href) : undefined}
            onClick={href ? () => onNavigate(link(href)) : undefined}
            current={current}
            trail={trail}
            testId={testId}
          />
        );
      }
      case "group": {
        const current = isCurrent(item, ctx);
        const open = isOpen(item.id, current);
        const children = childrenOf(item, ctx);
        const badge = item.badge?.(ctx);
        const groupSlot = slot(item.trail, { place: "row", depth: rowDepth(depth) });
        return (
          <div key={item.id} data-testid={`${testId}-group`} data-tour={item.tour}>
            <SidebarSubRow
              label={item.label}
              depth={rowDepth(depth)}
              icon={rowIcon(item, depth)}
              onClick={() => toggle(item.id, open)}
              expanded={open}
              trail={
                groupSlot || badge ? (
                  <>
                    {groupSlot}
                    {badge && <Badge badge={badge} />}
                  </>
                ) : undefined
              }
              testId={testId}
            />
            {open && (
              <div role="group" aria-label={item.label} className="space-y-0.5">
                {renderItems(children, deeper(depth), where === "footer" ? "footer" : "nav")}
              </div>
            )}
          </div>
        );
      }
      case "area": {
        // An area's row: its first page (a URL), with ›. Drawn only while the route is not in it, but
        // the footer's Settings stays and is lit while Settings is open.
        const href = hrefOf(item, ctx);
        const current = area?.id === item.id;
        const badge = current ? undefined : item.badge?.(ctx);
        const trail = (
          <>
            {!current && slot(item.trail, { place: "row", depth: rowDepth(depth) })}
            {badge && <Badge badge={badge} />}
            <ChevronRight className="size-3.5 text-muted-foreground" aria-hidden="true" />
          </>
        );
        if (where === "footer") {
          return (
            <SidebarRow
              key={item.id}
              icon={item.icon}
              label={item.label}
              href={href ? link(href) : undefined}
              onClick={href ? () => onNavigate(link(href), { drill: true }) : undefined}
              current={current}
              trail={trail}
              action={slot(item.action, { place: "row", depth: rowDepth(depth) })}
              testId={testId}
            />
          );
        }
        return (
          <SidebarSubRow
            key={item.id}
            label={item.label}
            depth={rowDepth(depth)}
            icon={rowIcon(item, depth)}
            href={href ? link(href) : undefined}
            onClick={href ? () => onNavigate(link(href), { drill: true }) : undefined}
            current={current}
            trail={trail}
            testId={testId}
          />
        );
      }
    }
  }

  let body: ReactNode;
  if (area) {
    const children = childrenOf(area, ctx);
    const fills = children.some((child) => child.kind === "slot" && child.slot === "chat-list");
    const back = link(backHref(ctx));
    const header = (
      <a
        href={back}
        onClick={(event) => {
          if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
          event.preventDefault();
          if (onBack) onBack();
          else onNavigate(back, { drill: true });
        }}
        data-testid="nav-back"
        // The drill-in's tour anchor: on an area's routes the nav draws no `nav-top` and no area row,
        // so a step that rings the nav there points here (Onboarding/JOURNEYS.md "Anchors").
        data-tour="nav-drill-in"
        aria-label={`Back from ${area.label}`}
        className="no-color mx-2 mb-1 flex h-[var(--row-h)] shrink-0 items-center gap-1.5 rounded-md px-2 text-ui font-medium text-foreground transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        <ChevronLeft className="size-4 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">{area.label}</span>
      </a>
    );
    body = (
      <div className="flex min-h-0 flex-1 flex-col pt-1" data-testid="nav-drill-in" data-area={area.id}>
        {header}
        {fills ? (
          <nav aria-label={area.label} className="flex min-h-0 flex-1 flex-col space-y-0.5 px-2 pb-2">
            {renderItems(children, 0, "drill")}
          </nav>
        ) : (
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-3" data-testid="sidebar-regions">
            <nav aria-label={area.label} className="space-y-0.5 px-2">
              {renderItems(children, 0, "drill")}
            </nav>
          </div>
        )}
      </div>
    );
  } else {
    body = (
      // Block flow, not flex: a flex column here let its rows shrink below their content.
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-3" data-testid="sidebar-regions">
        <nav className="mt-1 space-y-0.5 px-2" aria-label="Main navigation" data-tour="forge-console-nav" data-testid="nav-top">
          {renderItems(config.items, 0, "nav")}
        </nav>
      </div>
    );
  }

  return (
    <>
      {body}
      <SidebarFooter>
        {renderItems(config.footer, 0, "footer")}
      </SidebarFooter>
    </>
  );
}

/** A slot's box: in a drill-in the chat list fills the column, so its box does too. */
function SlotFrame({ id, children }: { id: string; children: ReactNode }) {
  if (!children) return null;
  return (
    <div data-testid={`nav-${id}`} className={id === "slot-chat-list" ? "flex min-h-0 flex-1 flex-col" : undefined}>
      {children}
    </div>
  );
}
