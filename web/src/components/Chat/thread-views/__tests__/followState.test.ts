import { describe, expect, it } from "vitest";
import { createFollowState } from "../followState";

// These tests drive the state machine through the exact callback ORDER Virtuoso
// uses, because the bug being guarded against is an ordering artifact: content
// grows -> atBottom flips false -> followOutput is consulted -> the scroll it
// requests fires isScrolling(true) while still not-at-bottom.

describe("follow state", () => {
  it("follows by default", () => {
    expect(createFollowState().shouldFollow()).toBe(true);
  });

  // The jitter regression. A streamed chunk taller than atBottomThreshold takes
  // the viewport out of "at bottom" for the duration of the follow-scroll. If
  // that scroll is not claimed as programmatic, isScrolling(true) is read as the
  // user scrolling away, follow mode switches off, and then switches back on
  // when the scroll lands — flapping once per chunk.
  it("keeps following when its own scroll fires isScrolling off-bottom", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    // Virtuoso consults followOutput while still at the bottom; it claims the
    // scroll it is about to perform.
    expect(follow.shouldFollow()).toBe(true);
    follow.beginProgrammaticScroll();

    // The chunk lands, taking us off-bottom, and the follow-scroll reports
    // itself while the viewport is still catching up.
    follow.setAtBottom(false);
    follow.noteScrolling(true);

    expect(follow.userScrolledUp).toBe(false);

    follow.noteScrolling(false);
    follow.setAtBottom(true);
    expect(follow.shouldFollow()).toBe(true);
  });

  // Virtuoso also auto-scrolls on its own when content grows under the viewport
  // (its internal SIZE_INCREASED correction). That one cannot be claimed in
  // advance, so while streaming an unexplained scroll must not count as intent.
  it("keeps following through Virtuoso's unannounced size-increase scroll", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    follow.setAtBottom(false);
    follow.noteScrolling(true);

    expect(follow.userScrolledUp).toBe(false);
    follow.setAtBottom(true);
    expect(follow.shouldFollow()).toBe(true);
  });

  // The counterpart: while streaming, intent comes from the input device.
  it("stops following when the user wheels up mid-stream", () => {
    const follow = createFollowState();
    follow.setStreaming(true);

    let t = 1000;
    for (const dy of [-10, -12, -14]) {
      follow.noteWheel(dy, t);
      t += 16;
    }
    follow.setAtBottom(false);
    follow.noteScrolling(true);

    expect(follow.shouldFollow()).toBe(false);
  });

  // When nothing is streaming, nothing else moves the viewport, so an
  // unexplained scroll off the bottom can only be the user.
  it("stops following when the user scrolls away unprompted", () => {
    const follow = createFollowState();
    follow.setAtBottom(false);
    // No beginProgrammaticScroll: this scroll is the user's.
    follow.noteScrolling(true);

    expect(follow.userScrolledUp).toBe(true);
    expect(follow.shouldFollow()).toBe(false);
  });

  it("resumes following once the user returns to the bottom", () => {
    const follow = createFollowState();
    follow.setAtBottom(false);
    follow.noteScrolling(true);
    expect(follow.shouldFollow()).toBe(false);

    follow.setAtBottom(true);
    expect(follow.shouldFollow()).toBe(true);
  });

  it("does not let one claimed scroll excuse the next user scroll", () => {
    const follow = createFollowState();
    follow.setAtBottom(false);
    follow.beginProgrammaticScroll();
    follow.noteScrolling(true);
    // The claimed scroll ends without reaching the bottom (content grew again).
    follow.noteScrolling(false);

    // The user now scrolls for real; the earlier claim must not still apply.
    follow.noteScrolling(true);
    expect(follow.shouldFollow()).toBe(false);
  });

  describe("wheel gestures", () => {
    // A trackpad flick DOWNWARD decelerates through an inertial tail that
    // contains small upward deltas. Judging each event alone read those as
    // intent to scroll up, so scrolling toward the bottom cancelled follow mode.
    it("ignores the upward tail of a downward flick", () => {
      const follow = createFollowState();
      let t = 1000;
      // Individual tail deltas exceed the old per-event threshold, but the
      // gesture is overwhelmingly downward, which is what matters.
      for (const dy of [40, 30, 18, 9, 4, -7, -9, -6]) {
        follow.noteWheel(dy, t);
        t += 16;
      }
      expect(follow.userScrolledUp).toBe(false);
    });

    it("treats a sustained upward gesture as intent", () => {
      const follow = createFollowState();
      let t = 1000;
      for (const dy of [-6, -8, -10]) {
        follow.noteWheel(dy, t);
        t += 16;
      }
      expect(follow.userScrolledUp).toBe(true);
    });

    it("ignores micro-jitter below the threshold", () => {
      const follow = createFollowState();
      follow.noteWheel(-3, 1000);
      follow.noteWheel(-2, 1016);
      expect(follow.userScrolledUp).toBe(false);
    });

    // Without a gesture boundary, small upward deltas from unrelated flicks
    // minutes apart would eventually sum past the threshold.
    it("does not accumulate across separate gestures", () => {
      const follow = createFollowState();
      follow.noteWheel(-8, 1000);
      follow.noteWheel(-8, 5000);
      expect(follow.userScrolledUp).toBe(false);
    });
  });

  // While streaming, an unexplained scroll is attributed to Virtuoso, so every
  // input device that can scroll the list needs its own intent signal. A device
  // with no signal cannot escape follow mode at all.
  describe("non-wheel input devices", () => {
    it("stops following when the user drags the transcript down mid-stream", () => {
      const follow = createFollowState();
      follow.setStreaming(true);

      // A finger moving DOWN the screen reveals earlier content; the caller
      // converts that to the wheel's negative convention.
      let t = 1000;
      for (const dy of [-9, -11, -13]) {
        follow.noteTouchMove(dy, t);
        t += 16;
      }
      follow.setAtBottom(false);
      follow.noteScrolling(true);

      expect(follow.shouldFollow()).toBe(false);
    });

    it("ignores the tail of a downward touch flick", () => {
      const follow = createFollowState();
      follow.setStreaming(true);

      let t = 1000;
      for (const dy of [38, 26, 15, 7, -6, -8]) {
        follow.noteTouchMove(dy, t);
        t += 16;
      }
      expect(follow.userScrolledUp).toBe(false);
    });

    it("treats a single scroll-up keypress as intent", () => {
      const follow = createFollowState();
      follow.setStreaming(true);

      // No threshold to clear: a keypress is discrete, not a gesture.
      follow.noteKeyScrollUp();
      follow.setAtBottom(false);
      follow.noteScrolling(true);

      expect(follow.shouldFollow()).toBe(false);
    });
  });

  // resumeFollow claims the scroll it is about to perform. If the list is
  // ALREADY at the bottom that scroll never happens, so the claim must not
  // survive to excuse the user's next, unrelated scroll.
  //
  // Asserted on userScrolledUp rather than shouldFollow: shouldFollow is false
  // here either way, because atBottom is false. That makes it useless as a
  // discriminator — it hides whether the scroll was correctly ATTRIBUTED. The
  // damage of a stale claim only surfaces later, when the viewport returns to
  // the bottom and an unset userScrolledUp silently re-enables following.
  it("does not let an unspent resumeFollow claim excuse a later user scroll", () => {
    let clock = 1000;
    const follow = createFollowState(() => clock);
    follow.resumeFollow();

    // The requested scroll produced no movement — nothing to follow — so the
    // claim is never spent. Time passes, and the user scrolls away for real.
    clock += 5000;
    follow.setAtBottom(false);
    follow.noteScrolling(true);

    expect(follow.userScrolledUp).toBe(true);
  });

  // The scroll-to-bottom button. scrollToIndex on a virtualized list is not one
  // scroll: Virtuoso jumps using ESTIMATED heights, measures what actually
  // rendered, and corrects — so one click produces several isScrolling cycles,
  // all of them still off-bottom. Only the first carries the claim, so if a
  // claim is consumed by the first scroll, the correction pass is blamed on the
  // user and follow mode dies the instant the button is pressed.
  it("keeps following across the correction passes of one button press", () => {
    let clock = 1000;
    const follow = createFollowState(() => clock);
    follow.setAtBottom(false);
    follow.noteScrolling(true);
    expect(follow.shouldFollow()).toBe(false);

    follow.resumeFollow();

    // Pass 1: the initial jump.
    clock += 16;
    follow.noteScrolling(true);
    follow.noteScrolling(false);

    // Pass 2: Virtuoso corrects after measuring real heights. Still off-bottom.
    clock += 16;
    follow.noteScrolling(true);
    follow.noteScrolling(false);

    expect(follow.userScrolledUp).toBe(false);

    // The scroll lands, and following is live again.
    follow.setAtBottom(true);
    expect(follow.shouldFollow()).toBe(true);
  });

  // The other side of the TTL: a claim IS honored for the scroll it was made
  // for. Without this, the expiry above could be "fixed" by never honoring a
  // claim at all, which reintroduces the original flap.
  it("honors a claim for the scroll that immediately follows it", () => {
    let clock = 1000;
    const follow = createFollowState(() => clock);
    follow.resumeFollow();

    // The scroll starts on the next frame, as a real one does.
    clock += 16;
    follow.setAtBottom(false);
    follow.noteScrolling(true);

    expect(follow.userScrolledUp).toBe(false);
  });

  it("resumeFollow re-enables following from a scrolled-up state", () => {
    const follow = createFollowState();
    follow.setAtBottom(false);
    follow.noteScrolling(true);
    expect(follow.shouldFollow()).toBe(false);

    follow.resumeFollow();
    // The button's own scroll must not re-arm the scrolled-up flag.
    follow.noteScrolling(true);
    expect(follow.userScrolledUp).toBe(false);
  });

  it("releaseFollow holds position for a jump-to-message", () => {
    const follow = createFollowState();
    follow.beginProgrammaticScroll();
    follow.releaseFollow();
    expect(follow.shouldFollow()).toBe(false);
  });
});
