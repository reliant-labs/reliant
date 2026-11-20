// Copyright (c) 2026 Reliant Labs

/**
 * THE ONE RENDERER for a nav config (navConfig/, U6c prototype): rows,
 * accordions, headings, slots, and drill-in areas.
 *
 * DRILL-IN. The route decides it (navConfig/model.ts activeArea): on a route
 * an area claims, the nav's rows are REPLACED by the area's, under a
 * "‹ Area" header that links back to the project's Overview. Nothing about it
 * is local state, so Back, refresh and a shared link land on the same nav.
 *
 * ACCORDIONS. Open or closed is local, but the group holding the page on
 * screen opens on every navigation, and clicking an open group closes it —
 * the current one too (owner bug 3).
 */

import { useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

import { externalMark } from "./navConfig/items";
import { activeArea, backHref, childrenOf, hrefOf, isCurrent, visibleItems } from "./navConfig/model";
import type { NavBadge, NavConfig, NavCtx, NavItem, NavSlotId } from "./navConfig/types";
import { withNavParam } from "./navConfig/variant";
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
  /** A variant was chosen (`?nav=` or the tab's): links carry it, for a new tab. */
  explicitVariant: boolean;
  slots: NavSlots;
  onNavigate: (href: string) => void;
  /** Under the footer's rows (the variant picker). */
  footerExtra?: ReactNode;
}

type Depth = 0 | 1 | 2 | 3;
const deeper = (depth: Depth): Depth => Math.min(depth + 1, 3) as Depth;
const testIdOf = (item: NavItem) => item.testId ?? `nav-${item.id}`;

function allGroups(items: readonly NavItem[], ctx: NavCtx): NavItem[] {
  return visibleItems(items, ctx).flatMap((item) => (item.kind === "group" || item.kind === "area" ? [item, ...allGroups(childrenOf(item, ctx), ctx)] : []));
}

function Badge({ badge }: { badge: NavBadge }) {
  if (badge.type === "dot") return <span role="img" aria-label={badge.label} className="size-2 rounded-full bg-warning" />;
  return (
    <span className={cn("tabular-nums", badge.tone === "danger" && "font-medium text-destructive-ink")}>
      {badge.value}
      {badge.label && <span className="sr-only"> {badge.label}</span>}
    </span>
  );
}

export function NavRenderer({ config, ctx, explicitVariant, slots, onNavigate, footerExtra }: NavRendererProps) {
  const area = activeArea(config, ctx);
  const link = (href: string) => (explicitVariant ? withNavParam(href, ctx.variant) : href);

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
    for (const item of visibleItems(items, ctx)) out.push(renderItem(item, depth, where));
    return out;
  }

  function renderItem(item: NavItem, depth: Depth, where: "nav" | "footer" | "drill"): ReactNode {
    const testId = testIdOf(item);
    switch (item.kind) {
      case "heading":
        return (
          <SidebarSubLabel key={item.id} depth={Math.min(depth, 2) as 0 | 1 | 2}>
            {item.label}
          </SidebarSubLabel>
        );
      case "slot": {
        const place: SlotPlace = where === "drill" && item.slot === "chat-list" ? "fill" : "capped";
        return <SlotFrame key={item.id} id={item.id}>{slot(item.slot, { place, depth })}</SlotFrame>;
      }
      case "row": {
        const href = hrefOf(item, ctx);
        const current = isCurrent(item, ctx);
        const badge = item.badge?.(ctx);
        const trail =
          badge || item.trail || item.external ? (
            <>
              {slot(item.trail, { place: "row", depth })}
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
              testId={testId}
            />
          );
        }
        return (
          <SidebarSubRow
            key={item.id}
            label={item.label}
            depth={depth}
            icon={depth === 0 ? item.icon : undefined}
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
        return (
          <div key={item.id} data-testid={`${testId}-group`}>
            <SidebarSubRow
              label={item.label}
              depth={depth}
              icon={depth === 0 ? item.icon : undefined}
              onClick={() => toggle(item.id, open)}
              expanded={open}
              trail={slot(item.trail, { place: "row", depth }) || undefined}
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
        const trail = (
          <>
            {!current && slot(item.trail, { place: "row", depth })}
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
              onClick={href ? () => onNavigate(link(href)) : undefined}
              current={current}
              trail={trail}
              action={slot(item.action, { place: "row", depth })}
              testId={testId}
            />
          );
        }
        return (
          <SidebarSubRow
            key={item.id}
            label={item.label}
            depth={depth}
            icon={depth === 0 ? item.icon : undefined}
            href={href ? link(href) : undefined}
            onClick={href ? () => onNavigate(link(href)) : undefined}
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
          onNavigate(back);
        }}
        data-testid="nav-back"
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
        {footerExtra}
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
