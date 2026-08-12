/**
 * Request that the chat timeline scroll to a specific message.
 *
 * The timeline listens for the `scroll-to-message` event and resolves the id to
 * a virtualized index. Two things make a single dispatch unreliable:
 *
 *   1. After navigating to a different chat, the target timeline has not
 *      mounted yet, so nothing is listening when the event fires.
 *   2. Messages load in pages. A hit near the top of a long conversation may
 *      not be in the loaded window on the first frame.
 *
 * So we re-dispatch on a short interval until the timeline confirms it handled
 * the request, then stop. The listener acknowledges by calling
 * `acknowledgeScrollToMessage`, which keeps this polling bounded — without an
 * ack we would either give up too early on a slow mount or keep firing after a
 * successful scroll and yank the user back if they scrolled away.
 */

const RETRY_INTERVAL_MS = 100;
const MAX_ATTEMPTS = 20; // ~2s, enough for a chat switch plus a page fetch.

let activeRequest: { messageId: string; cancel: () => void } | null = null;

export function requestScrollToMessage(messageId: string): void {
  // A newer request supersedes an older one.
  activeRequest?.cancel();

  let attempts = 0;
  let timer: ReturnType<typeof setInterval> | undefined;

  const stop = () => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
    if (activeRequest?.messageId === messageId) activeRequest = null;
  };

  const attempt = () => {
    attempts += 1;
    if (attempts > MAX_ATTEMPTS) {
      stop();
      return;
    }
    window.dispatchEvent(
      new CustomEvent("scroll-to-message", { detail: { messageId } }),
    );
  };

  activeRequest = { messageId, cancel: stop };
  attempt();
  timer = setInterval(attempt, RETRY_INTERVAL_MS);
}

/**
 * Called by the timeline once it has scrolled to the message, to stop retries.
 */
export function acknowledgeScrollToMessage(messageId: string): void {
  if (activeRequest?.messageId === messageId) {
    activeRequest.cancel();
  }
}
