// Copyright (c) 2025 Reliant Labs

/**
 * Moves keyboard focus to a row of a VIRTUALIZED list once that row exists.
 *
 * An arrow key past the rendered window scrolls the virtualizer first, and the
 * target row is only mounted on a later render — so focusing it synchronously
 * finds nothing. The request is kept until a commit renders the row, and
 * applied from an effect (not requestAnimationFrame, which a background tab
 * never runs).
 */

import { useEffect, useState, type RefObject } from "react";

export function useRovingFocus(containerRef: RefObject<HTMLElement | null>, attribute: string) {
  const [pending, setPending] = useState<number | null>(null);

  // No dependency list on purpose: the row may appear on any later render. It
  // cannot loop — setPending(null) runs once, and a null request returns early.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (pending === null) return;
    const row = containerRef.current?.querySelector<HTMLElement>(`[${attribute}="${pending}"]`);
    if (!row) return;
    row.focus();
    setPending(null);
  });

  return setPending;
}
