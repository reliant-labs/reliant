// Copyright (c) 2026 Reliant Labs

/**
 * Reading a nav config against the route: which items show, where each goes,
 * which is current, and which drill-in (if any) the route is in. Pure, so the
 * renderer and the tests read the same answers.
 */

import { projectPath, projectsPath } from "@/lib/paths";

import { consolePath } from "../../../Forge/Console/paths";
import type { NavArea, NavBadge, NavBadgeOf, NavConfig, NavCtx, NavItem } from "./types";

/**
 * The usual live badge: the console's count from `source`
 * (GroupedNavInput.navCounts), labelled for a screen reader; nothing at 0 or
 * before the console model answers.
 *
 *   badge: countBadge("tests", "suspected or flaky test", "suspected or flaky tests")
 */
export function countBadge(source: string, one: string, many: string, tone?: "danger"): NavBadgeOf {
  return (ctx: NavCtx): NavBadge | undefined => {
    const count = ctx.console?.navCounts?.[source] ?? 0;
    return count > 0 ? { type: "count", value: count, label: count === 1 ? one : many, ...(tone ? { tone } : {}) } : undefined;
  };
}

export function isAvailable(item: NavItem, ctx: NavCtx): boolean {
  return item.available?.(ctx) ?? true;
}

/** A group's or area's rows for this context, the unavailable ones left out. */
export function childrenOf(item: NavItem, ctx: NavCtx): readonly NavItem[] {
  if (item.kind !== "group" && item.kind !== "area") return [];
  const children = typeof item.children === "function" ? item.children(ctx) : item.children;
  return children.filter((child) => isAvailable(child, ctx));
}

/** The items of a list that show for this context. */
export function visibleItems(items: readonly NavItem[], ctx: NavCtx): readonly NavItem[] {
  return items.filter((item) => isAvailable(item, ctx));
}

/** Where an item goes: a row's page; an area's own, else its first row's. A group goes nowhere (it opens in place). */
export function hrefOf(item: NavItem, ctx: NavCtx): string | undefined {
  if (item.kind === "row") return item.href(ctx);
  if (item.kind === "area") {
    const own = item.href?.(ctx);
    if (own) return own;
    for (const child of childrenOf(item, ctx)) {
      const href = child.kind === "group" ? childrenOf(child, ctx).map((row) => hrefOf(row, ctx)).find(Boolean) : hrefOf(child, ctx);
      if (href) return href;
    }
  }
  return undefined;
}

/** Whether the item is the page on screen, or (a group, an area) holds it. */
export function isCurrent(item: NavItem, ctx: NavCtx): boolean {
  switch (item.kind) {
    case "row":
      return item.match?.(ctx) ?? false;
    case "area":
      return item.match(ctx);
    case "group":
      return (item.match?.(ctx) ?? false) || childrenOf(item, ctx).some((child) => isCurrent(child, ctx));
    default:
      return false;
  }
}

/**
 * What takes the nav column on this route: the first available drill-in area
 * (top to bottom, then the footer) whose routes include it, else a section
 * page's own list (Settings). Null: the top level.
 */
export function activeArea(config: NavConfig, ctx: NavCtx): NavArea | null {
  for (const item of [...config.items, ...config.footer, ...(config.sectionPages ?? [])]) {
    if (item.kind === "area" && isAvailable(item, ctx) && item.match(ctx)) return item;
  }
  return null;
}

/** The drill-in areas a config draws as rows (›): the areas among its items and footer. */
export function drillInIds(config: NavConfig): string[] {
  return [...config.items, ...config.footer].filter((item) => item.kind === "area").map((item) => item.id);
}

/** Every id a config draws for this context, depth first (tests: Dashboards appears once). */
export function flattenIds(items: readonly NavItem[], ctx: NavCtx): string[] {
  return visibleItems(items, ctx).flatMap((item) => [item.id, ...flattenIds(childrenOf(item, ctx), ctx)]);
}

/**
 * Where a drill-in's "‹ Area" header goes: the project's Overview (its app's
 * landing page, or the project's page with no app), or the projects' summary
 * with several in view.
 */
export function backHref(ctx: NavCtx): string {
  if (ctx.mode !== "one") return projectsPath();
  if (ctx.app) return consolePath(ctx.app, { kind: "overview" });
  if (ctx.projectId) return projectPath(ctx.projectId);
  return projectsPath();
}
