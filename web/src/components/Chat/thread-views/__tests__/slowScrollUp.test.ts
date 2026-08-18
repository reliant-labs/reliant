/**
 * Slow scroll-up must release follow mode, exactly as a fast one does.
 *
 * The reported jitter is speed-dependent: a quick flick up reads fine, a slow
 * scroll up fights back. Both halves of that asymmetry come from the same
 * place.
 *
 * A slow scroll is small nudges spaced far apart. Each nudge moves the
 * viewport less than Virtuoso's atBottomThreshold (80px), so `atBottom` never
 * flips and `shouldFollow()` stays live — which means the very next streamed
 * delta scrolls the transcript back down and undoes the nudge. The only thing
 * that can suppress that is `userScrolledUp`, and while streaming the only
 * signal that can set it is the wheel gesture accumulator below.
 *
 * A fast flick clears the threshold inside one gesture, so it escapes on both
 * counts and never jitters. That is why the bug only shows up when scrolling
 * slowly.
 */

import { describe, expect, it } from "vitest";
import { createFollowState } from "../followState";

/** Virtuoso's atBottomThreshold — a scroll shorter than this stays "at bottom". */
const AT_BOTTOM_THRESHOLD_PX = 80;

describe("slow scroll-up during streaming", () => {
  // The reproduction. A deliberate, sustained slow scroll up: nudges well
  // apart in time, each one small enough to keep the viewport "at bottom".
  // Nothing here is ambiguous — the user is scrolling up and has not stopped —
  // yet no single nudge clears the threshold on its own.
  it("releases follow for a sustained slow scroll made of small nudges", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    // Six nudges of 8px, 300ms apart: 48px total, comfortably inside the
    // at-bottom threshold, over nearly two seconds of continuous scrolling.
    let t = 1000;
    let travelled = 0;
    for (let i = 0; i < 6; i++) {
      follow.noteWheel(-8, t);
      travelled += 8;
      t += 300;
    }

    // Precondition for the bug: the viewport never left "at bottom", so
    // atBottom cannot be what stops follow mode. userScrolledUp has to.
    expect(travelled).toBeLessThan(AT_BOTTOM_THRESHOLD_PX);
    expect(follow.atBottom).toBe(true);

    expect(follow.userScrolledUp).toBe(true);
    expect(follow.shouldFollow()).toBe(false);
  });

  // The same gesture at trackpad granularity: many tiny deltas, still clearly
  // one continuous upward scroll.
  it("releases follow for a slow trackpad drag of sub-threshold deltas", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    let t = 1000;
    for (let i = 0; i < 10; i++) {
      follow.noteWheel(-4, t);
      t += 200;
    }

    expect(follow.userScrolledUp).toBe(true);
  });

  // The fast case, which the user reports as working. Pinned so a fix for the
  // slow case cannot be mistaken for one that simply lowers the bar for
  // everything.
  it("still releases follow for a fast flick up", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    let t = 1000;
    for (const dy of [-30, -40, -50]) {
      follow.noteWheel(dy, t);
      t += 16;
    }

    expect(follow.userScrolledUp).toBe(true);
  });

  // The guard that makes the slow-scroll fix non-trivial: a downward flick
  // decelerates through an inertial tail of small UPWARD deltas. Those arrive
  // in rapid succession, and must still not read as intent to scroll up.
  it("still ignores the upward tail of a downward flick", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    let t = 1000;
    for (const dy of [40, 30, 18, 9, 4, -7, -9, -6]) {
      follow.noteWheel(dy, t);
      t += 16;
    }

    expect(follow.userScrolledUp).toBe(false);
  });

  // A downward flick must not bank credit that immunizes a LATER upward
  // scroll. Carrying the accumulator forward naively would do exactly that:
  // the user flicks down, then starts slowly scrolling up, and the leftover
  // positive sum swallows the first several nudges.
  it("does not let an earlier downward flick swallow a later slow scroll up", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    let t = 1000;
    for (const dy of [40, 30, 18, 9]) {
      follow.noteWheel(dy, t);
      t += 16;
    }

    // A beat later, the user changes their mind and scrolls slowly up.
    t += 400;
    for (let i = 0; i < 4; i++) {
      follow.noteWheel(-8, t);
      t += 300;
    }

    expect(follow.userScrolledUp).toBe(true);
  });

  // Two isolated nudges minutes apart are not a gesture. Without a true idle
  // reset, stray deltas would eventually sum past the threshold on their own.
  it("does not accumulate across a long idle gap", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    follow.noteWheel(-8, 1000);
    follow.noteWheel(-8, 30_000);

    expect(follow.userScrolledUp).toBe(false);
  });
});
