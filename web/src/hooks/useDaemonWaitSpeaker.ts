/**
 * Which "your machine isn't ready" surface gets to say it in full.
 *
 * When the machine is down, every daemon-backed surface on screen is blocked
 * at once — the chat composer, the file tree, the editor, the terminal — and
 * each renders a `DaemonWaitState`. Rendered in full, that is the same
 * headline, the same paragraph and the same two buttons three or four times
 * over, a few hundred pixels apart, and announced to a screen reader once per
 * copy.
 *
 * Every surface still has to say that it's blocked: an empty file tree with no
 * explanation is worse than repetition. But only one of them needs to say WHY
 * and offer the exits. This module elects that one — the speaker — and the
 * rest collapse to a one-line echo of the headline.
 *
 * The election:
 *   - A primary surface (the chat composer strip, the connect modal, the
 *     onboarding gate) always speaks. Its whole region exists to carry this
 *     message, and secondary surfaces defer to it while it's visible.
 *   - Otherwise the highest-ranked visible surface speaks: a panel has room for
 *     the full message, a terminal overlay does not. Ties go to whichever
 *     mounted first, so the speaker doesn't hop around as surfaces re-render.
 *   - Only VISIBLE surfaces are eligible. Terminals stay mounted while hidden
 *     (it keeps their shells alive), and without this the speaker could be a
 *     terminal nobody can see while every visible surface defers to it.
 *
 * Because the speaker is the only one with a "Try again" button, its retry
 * fans out to every secondary surface. The machine is shared, so a retry from
 * one place is a retry for all of them — otherwise a deferring surface whose
 * own button is hidden could stay stuck after the machine came back.
 */

import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";

interface Claim {
  rank: number;
  secondary: boolean;
  visible: boolean;
  /** Mount order, so ties break the same way on every render. */
  order: number;
  retry: { current: (() => void) | undefined };
}

const claims = new Map<string, Claim>();
const listeners = new Set<() => void>();
let speakerId: string | null = null;
let nextOrder = 0;

function elect(): string | null {
  let bestId: string | null = null;
  let best: Claim | null = null;
  for (const [id, claim] of claims) {
    if (!claim.visible) continue;
    if (!best || claim.rank > best.rank || (claim.rank === best.rank && claim.order < best.order)) {
      best = claim;
      bestId = id;
    }
  }
  return bestId;
}

function publish() {
  const next = elect();
  if (next === speakerId) return;
  speakerId = next;
  for (const listener of listeners) listener();
}

export interface UseDaemonWaitSpeakerOptions {
  /** Higher outranks lower among visible surfaces. */
  rank: number;
  /** Secondary surfaces defer to a better visible surface; primary ones never do. */
  secondary: boolean;
  /** This surface's own retry. The speaker's retry invokes it too. */
  onRetry?: () => void;
}

export interface UseDaemonWaitSpeakerResult {
  /** Render the full message (detail, reason, actions) rather than the echo. */
  speaks: boolean;
  /** Attach to the surface's root element so its visibility can be tracked. */
  ref: (element: HTMLElement | null) => void;
  /** Retry this surface and every secondary surface deferring to the speaker. */
  retry: () => void;
}

export function useDaemonWaitSpeaker({
  rank,
  secondary,
  onRetry,
}: UseDaemonWaitSpeakerOptions): UseDaemonWaitSpeakerResult {
  const id = useId();
  const [order] = useState(() => nextOrder++);
  const [element, setElement] = useState<HTMLElement | null>(null);
  const [speaker, setSpeaker] = useState<string | null>(speakerId);

  const retryRef = useRef(onRetry);
  useLayoutEffect(() => {
    retryRef.current = onRetry;
  }, [onRetry]);

  // Visible until shown otherwise: assuming hidden would render the lone
  // surface — the common case — as an echo for a frame before it speaks.
  const visibleRef = useRef(true);

  // Subscribed in a LAYOUT effect, ahead of registration below, so the
  // election this surface's own registration triggers re-renders it before
  // paint. A passive subscription would show one frame of echo first.
  useLayoutEffect(() => {
    const listener = () => setSpeaker(speakerId);
    listeners.add(listener);
    listener();
    return () => {
      listeners.delete(listener);
    };
  }, []);

  useLayoutEffect(() => {
    claims.set(id, { rank, secondary, order, visible: visibleRef.current, retry: retryRef });
    publish();
    return () => {
      claims.delete(id);
      publish();
    };
  }, [id, order, rank, secondary]);

  useEffect(() => {
    if (!element || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver((entries) => {
      const entry = entries[entries.length - 1];
      if (!entry) return;
      visibleRef.current = entry.isIntersecting;
      const claim = claims.get(id);
      if (claim && claim.visible !== entry.isIntersecting) {
        claim.visible = entry.isIntersecting;
        publish();
      }
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [element, id]);

  const retry = useCallback(() => {
    retryRef.current?.();
    for (const [otherId, claim] of claims) {
      if (otherId !== id && claim.secondary) claim.retry.current?.();
    }
  }, [id]);

  return { speaks: !secondary || speaker === id, ref: setElement, retry };
}
