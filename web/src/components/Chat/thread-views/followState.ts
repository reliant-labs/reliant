/**
 * Scroll-follow state for the chat timeline.
 *
 * "Follow mode" is the behavior where the transcript sticks to the bottom as
 * new content streams in, and releases when the user deliberately scrolls away
 * to read something earlier.
 *
 * The whole difficulty is telling those two apart, because Virtuoso reports
 * them through the same callbacks. This lives outside the component so the
 * decisions can be exercised directly rather than through a virtualized list
 * that does not lay out in jsdom.
 */

/** A wheel movement smaller than this (in px, net over a gesture) is noise. */
const WHEEL_UP_THRESHOLD = 12;

/** Wheel events separated by more than this start a new gesture. */
const GESTURE_GAP_MS = 150;

/**
 * How long a programmatic-scroll claim stays valid.
 *
 * A claim must outlive the WHOLE scroll it describes, and a scrollToIndex on a
 * virtualized list settles over several frames: Virtuoso jumps using estimated
 * row heights, measures what really rendered, and corrects — sometimes more
 * than once when rows are far from their estimates. Every one of those passes
 * reports isScrolling while still off-bottom, and all of them belong to the
 * claim.
 *
 * It must also be short enough that a claim whose scroll never happened at all
 * (resumeFollow when the list is already at the bottom, so there is nothing to
 * move) cannot excuse a later, genuine user scroll. Half a second clears a
 * multi-pass correction with room to spare while staying at the floor of
 * "notice the list moved, decide to scroll away, act" — and the common cases
 * never reach the deadline anyway, since arriving at the bottom clears the
 * claim outright.
 */
const PROGRAMMATIC_CLAIM_TTL_MS = 500;

export interface FollowState {
  /** Whether new content should pull the viewport down. */
  shouldFollow(): boolean;
  /** Virtuoso's atBottomStateChange. */
  setAtBottom(atBottom: boolean): void;
  /** Virtuoso's isScrolling. */
  noteScrolling(scrolling: boolean): void;
  /** Whether the assistant is currently producing content. */
  setStreaming(streaming: boolean): void;
  /**
   * Declare that the scroll about to happen is ours, not the user's. Must be
   * called before returning a behavior from followOutput.
   */
  beginProgrammaticScroll(): void;
  /** A wheel event over the transcript, while streaming. */
  noteWheel(deltaY: number, timeStamp: number): void;
  /**
   * A touch drag over the transcript, while streaming. `deltaY` uses the wheel
   * sign convention (negative = moving toward earlier content), so a finger
   * moving DOWN the screen is a negative delta.
   */
  noteTouchMove(deltaY: number, timeStamp: number): void;
  /** A key that scrolls toward earlier content (ArrowUp, PageUp, Home). */
  noteKeyScrollUp(): void;
  /** Re-enable following, e.g. the scroll-to-bottom button or a thread switch. */
  resumeFollow(): void;
  /** Suppress following, e.g. restoring a saved mid-conversation position. */
  releaseFollow(): void;
  /** Test/debug view of the internals. */
  readonly atBottom: boolean;
  readonly userScrolledUp: boolean;
}

/**
 * @param now Monotonic clock in milliseconds, used to expire stale
 *   programmatic-scroll claims. Injectable so tests can advance time without
 *   waiting; defaults to the same time origin as DOM event timestamps.
 */
export function createFollowState(
  now: () => number = () =>
    typeof performance !== "undefined" ? performance.now() : Date.now(),
): FollowState {
  let atBottom = true;
  let userScrolledUp = false;
  let streaming = false;
  let gestureDelta = 0;
  let lastWheelAt = 0;

  // When the outstanding programmatic-scroll claim was made, or null if there
  // is none. A timestamp rather than a boolean so an unspent claim expires
  // instead of latching — see PROGRAMMATIC_CLAIM_TTL_MS.
  let programmaticClaimAt: number | null = null;

  function hasLiveClaim(): boolean {
    return (
      programmaticClaimAt !== null &&
      now() - programmaticClaimAt <= PROGRAMMATIC_CLAIM_TTL_MS
    );
  }

  // Judge the gesture, not the event. A trackpad flick or touch drag toward the
  // bottom decelerates through an inertial tail containing small upward deltas,
  // so testing each event alone reads scrolling TO the bottom as scrolling away
  // from it. Shared by wheel and touch, which differ only in how the caller
  // derives deltaY.
  function noteGesture(deltaY: number, timeStamp: number) {
    if (timeStamp - lastWheelAt > GESTURE_GAP_MS) {
      gestureDelta = 0;
    }
    lastWheelAt = timeStamp;
    gestureDelta += deltaY;
    if (gestureDelta < -WHEEL_UP_THRESHOLD) {
      userScrolledUp = true;
    }
  }

  return {
    shouldFollow() {
      return !userScrolledUp && atBottom;
    },

    setAtBottom(next: boolean) {
      atBottom = next;
      // Arriving at the bottom is unambiguous: whatever the user was reading
      // earlier, they are caught up now, so following resumes.
      if (next) {
        userScrolledUp = false;
        programmaticClaimAt = null;
      }
    },

    noteScrolling(scrolling: boolean) {
      // A scroll while away from the bottom means the user is navigating —
      // UNLESS it is one of ours. Content taller than atBottomThreshold takes
      // us out of "at bottom" for the duration of a follow-scroll, so treating
      // every such scroll as the user's made follow mode flap off and back on
      // once per large streamed chunk. That flap is the jitter.
      //
      // Two things can start a scroll we should not blame on the user, and only
      // the first is announceable: followOutput (which calls
      // beginProgrammaticScroll), and Virtuoso's internal SIZE_INCREASED
      // correction, which fires on its own when content grows under the
      // viewport and reports nothing we can subscribe to. While streaming,
      // therefore, isScrolling alone is not evidence of intent — a wheel or
      // touch gesture is required, and noteWheel supplies it.
      const selfScroll = hasLiveClaim() || streaming;
      if (scrolling && !atBottom && !selfScroll) {
        userScrolledUp = true;
      }
      // A claim covers a WINDOW of time, not a single scroll, and is cleared by
      // arriving at the bottom (setAtBottom) or by expiring — never by being
      // "used up" here.
      //
      // scrollToIndex on a virtualized list is not one scroll. Virtuoso jumps
      // using estimated row heights, measures what actually rendered, and
      // corrects, so one press of the scroll-to-bottom button produces several
      // isScrolling cycles that are all still off-bottom. Retiring the claim on
      // the first of them blamed the correction pass on the user and switched
      // follow mode off on every press — the button appeared to do nothing.
    },

    setStreaming(next: boolean) {
      streaming = next;
    },

    beginProgrammaticScroll() {
      programmaticClaimAt = now();
    },

    noteWheel(deltaY: number, timeStamp: number) {
      noteGesture(deltaY, timeStamp);
    },

    noteTouchMove(deltaY: number, timeStamp: number) {
      // Same gesture accumulation as the wheel: a touch flick decelerates
      // through a tail too, and the caller has already converted the drag into
      // the wheel's sign convention.
      noteGesture(deltaY, timeStamp);
    },

    noteKeyScrollUp() {
      // A keypress is discrete, not a gesture — there is no tail to average
      // away and no threshold to clear. One ArrowUp is intent.
      userScrolledUp = true;
      gestureDelta = 0;
    },

    resumeFollow() {
      userScrolledUp = false;
      programmaticClaimAt = now();
      gestureDelta = 0;
    },

    releaseFollow() {
      userScrolledUp = true;
    },

    get atBottom() {
      return atBottom;
    },
    get userScrolledUp() {
      return userScrolledUp;
    },
  };
}
