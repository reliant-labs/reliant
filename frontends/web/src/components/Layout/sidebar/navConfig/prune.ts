// Copyright (c) 2026 Reliant Labs

/**
 * A nav without the pages its host does not serve. The public demo serves
 * no Usage (Forge/Demo/demoPath.ts DEMO_HIDDEN_PAGES):
 * it draws the app's own items with those rows pruned, so no row leads to a
 * page that is not there.
 *
 * A pure transform of the config: a row goes when its `page` (the console
 * page kind it opens) is one of `kinds`; a group or area left with nothing to
 * open goes with it. A group whose rows are built per context (Analytics,
 * Security) is pruned when the nav draws it. The config passed in is never
 * changed, and with no kinds it comes back as it is.
 */

import type { ConsolePage } from "../../../Forge/Console/paths";
import type { NavArea, NavChildren, NavConfig, NavCtx, NavItem } from "./types";

export function withoutPages(config: NavConfig, kinds: readonly ConsolePage["kind"][]): NavConfig {
  if (kinds.length === 0) return config;
  const hidden = new Set<ConsolePage["kind"]>(kinds);

  const pruneItems = (items: readonly NavItem[]): NavItem[] => items.flatMap(pruneItem);

  function pruneItem(item: NavItem): NavItem[] {
    if (item.kind === "row") return item.page && hidden.has(item.page) ? [] : [item];
    if (item.kind !== "group" && item.kind !== "area") return [item];
    const children: NavChildren = item.children;
    if (typeof children !== "function") {
      const kept = pruneItems(children);
      // Nothing left to open: the group (or area) goes too.
      return kept.length === 0 && children.length > 0 ? [] : [{ ...item, children: kept }];
    }
    const prunedOf = (ctx: NavCtx) => pruneItems(children(ctx));
    return [
      {
        ...item,
        children: prunedOf,
        // Drawn where it was, unless the pruning left it nothing to open there.
        available: (ctx: NavCtx) => (item.available?.(ctx) ?? true) && (children(ctx).length === 0 || prunedOf(ctx).length > 0),
      },
    ];
  }

  const pruned: NavConfig = { items: pruneItems(config.items), footer: pruneItems(config.footer) };
  if (config.sectionPages) pruned.sectionPages = config.sectionPages.flatMap(pruneItem).filter((item): item is NavArea => item.kind === "area");
  return pruned;
}
