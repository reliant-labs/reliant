/**
 * Retry a one-shot daemon action across a machine that is still coming up.
 *
 * The rendered surfaces (file tree, terminal, editor) can sit in a waiting
 * state and re-issue their read on a cadence — `useDaemonWait` covers those.
 * An *action* can't: the user pressed send once, and the only honest options
 * are to complete it or to tell them it didn't happen.
 *
 * Chat used to take the third option, which was neither: it caught the
 * `no daemon connected` error and toasted "Please resend in a moment", making
 * the user the retry loop for a condition that resolves itself in under a
 * minute. Worse, the toast fires the instant a machine is asleep, so the
 * common case — open a chat, machine suspended, type — read as a failure of
 * the thing the user just did.
 *
 * This retries the action itself while the machine comes up, and only reports
 * failure when the machine genuinely won't serve.
 */

import { isDaemonConnectingError, wakingDaemonId } from "./daemon-errors";
import { DAEMON_WAIT_POLL_MS } from "./daemon-wait";

/**
 * How long to keep retrying a user-initiated action.
 *
 * Unlike the rendered waits — which escalate their copy but never give up,
 * because the user can see them and walk away — an action holds a composer
 * hostage, so it needs an outer bound. Two minutes covers a cold start
 * (image pull + clone) with room to spare; past that the user deserves to
 * hear that it didn't go through rather than watch a spinner indefinitely.
 */
export const DAEMON_ACTION_RETRY_MS = 120_000;

export interface SendWithDaemonWaitOptions<T = void> {
  /** The action to perform. Re-invoked on each attempt. */
  action: () => Promise<T>;
  /**
   * Called the first time the action is deferred, so the surface can show
   * that the message is waiting on the machine rather than sent.
   */
  onWaiting?: () => void;
  /** Called once the action finally succeeds after having been deferred. */
  onResolved?: () => void;
  /** Overall budget. Defaults to `DAEMON_ACTION_RETRY_MS`. */
  timeoutMs?: number;
  signal?: AbortSignal;
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    if (signal?.aborted) return reject(new DOMException("Aborted", "AbortError"));
    const id = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(id);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });

/**
 * Run `action`, transparently retrying while the machine is unavailable.
 *
 * Only the `no daemon connected` class is retried. Every other error — auth,
 * validation, a genuine backend fault — propagates on the first attempt,
 * because those don't get better by asking again and the user needs to see
 * them immediately.
 */
export async function sendWithDaemonWait<T = void>({
  action,
  onWaiting,
  onResolved,
  timeoutMs = DAEMON_ACTION_RETRY_MS,
  signal,
}: SendWithDaemonWaitOptions<T>): Promise<T> {
  const startedAt = Date.now();
  let deferred = false;

  for (;;) {
    try {
      const result = await action();
      if (deferred) onResolved?.();
      return result;
    } catch (error) {
      if (!isDaemonConnectingError(error)) throw error;

      // Out of budget — surface the original error so the caller's normal
      // error handling reports something true rather than our paraphrase.
      if (Date.now() - startedAt >= timeoutMs) throw error;

      if (!deferred) {
        deferred = true;
        onWaiting?.();
      }
      await sleep(DAEMON_WAIT_POLL_MS, signal);
    }
  }
}

export interface RetryAcrossWakeOptions {
  /**
   * Called once, when the first attempt found the machine asleep and the
   * server woke it, so the surface can say "Waking up…" instead of failing.
   */
  onWaking?: (daemonId: string) => void;
  /** Overall budget for the retries. Defaults to `DAEMON_ACTION_RETRY_MS`. */
  timeoutMs?: number;
  signal?: AbortSignal;
}

/**
 * Run a one-shot action (a commit, a push, creating or deleting a workspace)
 * that may find its machine asleep.
 *
 * When the server woke the machine for this action — the error names the
 * machine it woke (`wakingDaemonId`) — the action did not run, so it is retried
 * until the machine is up, within the same bound as `sendWithDaemonWait`. Any
 * other failure is thrown at once, as before: in particular a machine that is
 * offline and was NOT woken (a self-hosted laptop that is shut) does not hold
 * the action for two minutes on a machine nothing is bringing back.
 */
export async function retryAcrossWake<T>(
  action: () => Promise<T>,
  { onWaking, timeoutMs = DAEMON_ACTION_RETRY_MS, signal }: RetryAcrossWakeOptions = {},
): Promise<T> {
  try {
    return await action();
  } catch (error) {
    const daemonId = wakingDaemonId(error);
    if (!daemonId) throw error;
    onWaking?.(daemonId);
    // The machine is mid cold start; asking again immediately only fails.
    await sleep(DAEMON_WAIT_POLL_MS, signal);
    return sendWithDaemonWait({ action, timeoutMs, signal });
  }
}
