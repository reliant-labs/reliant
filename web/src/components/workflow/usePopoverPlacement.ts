// Copyright (c) 2025 Reliant Labs

import { useLayoutEffect, useState, type RefObject } from "react";

export type PopoverPlacement = "top" | "bottom";

/** The nearest ancestor that clips what overflows it (the config panel's scroll body). */
function clippingAncestor(element: HTMLElement): HTMLElement | null {
  for (let node = element.parentElement; node; node = node.parentElement) {
    const { overflowY } = getComputedStyle(node);
    if (overflowY === "auto" || overflowY === "scroll" || overflowY === "hidden") return node;
  }
  return null;
}

/**
 * Where a popover anchored under `anchor` should open: below it, unless the
 * room below — to the edge of whatever clips it, or the window — is less than
 * the popover needs and there is more room above. A picker on the last field
 * of a config panel otherwise opens into the panel's clipped edge.
 *
 * Measured once per opening, so the list does not jump while it is searched.
 */
export function usePopoverPlacement(anchor: RefObject<HTMLElement | null>, open: boolean, neededHeight: number): PopoverPlacement {
  const [placement, setPlacement] = useState<PopoverPlacement>("bottom");
  useLayoutEffect(() => {
    const element = anchor.current;
    if (!open || !element) return;
    const rect = element.getBoundingClientRect();
    const clip = clippingAncestor(element)?.getBoundingClientRect();
    const top = Math.max(clip?.top ?? 0, 0);
    const bottom = Math.min(clip?.bottom ?? window.innerHeight, window.innerHeight);
    const below = bottom - rect.bottom;
    const above = rect.top - top;
    setPlacement(below < neededHeight && above > below ? "top" : "bottom");
  }, [anchor, open, neededHeight]);
  return placement;
}
