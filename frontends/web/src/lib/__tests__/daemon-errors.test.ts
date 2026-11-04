/**
 * Confirms the backend's "daemon record exists but hasn't connected yet"
 * signal (toolexec.ErrDaemonPending, internal/toolexec/daemon_router.go)
 * classifies as a retryable wait, not a raw error — which is the whole
 * point of the fix: before it, the router's flat "no daemon available for
 * user X" error fell through this classifier and rendered verbatim. The
 * signal is the reason (daemon_starting, no_daemon), never the text.
 */
import { describe, expect, it } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";

import { isDaemonConnectingError, isNoMachineError } from "../daemon-errors";
import { classifyDaemonWait } from "../daemon-wait";

// The exact message toolexec.ErrDaemonPending wraps its errors with (see
// resolveDaemonID in internal/toolexec/daemon_router_nats.go), as it would
// arrive after crossing a Connect boundary as CodeUnavailable.
const PENDING_DAEMON_MESSAGE =
  "resolving daemon for command: daemon for user 94e41478-542b-4fd9-a68a-25d66de92c4f: no daemon connected: daemon record exists but has not registered yet (still starting)";

// The OLD flat error the router used to return for every unresolved case —
// used to prove the classifier correctly does NOT treat this shape as a wait.
const GENUINE_NO_DAEMON_MESSAGE =
  "resolving daemon for command: no daemon available for user 94e41478-542b-4fd9-a68a-25d66de92c4f";

function withReason(message: string, code: Code, reason: string): ConnectError {
  return new ConnectError(message, code, new Headers({ "x-forge-error-reason": reason }));
}

describe("isDaemonConnectingError classifies the pending-daemon signal", () => {
  it("treats ErrDaemonPending (reason daemon_starting) as a connecting/waiting error", () => {
    const err = withReason(PENDING_DAEMON_MESSAGE, Code.Unavailable, "daemon_starting");
    expect(isDaemonConnectingError(err)).toBe(true);
  });

  it("does NOT treat the old flat 'no daemon available' message as connecting", () => {
    const err = new ConnectError(GENUINE_NO_DAEMON_MESSAGE, Code.Unavailable);
    expect(isDaemonConnectingError(err)).toBe(false);
  });

  it("does not read the message: the old marker text alone is not a wait", () => {
    expect(isDaemonConnectingError(new ConnectError(PENDING_DAEMON_MESSAGE, Code.Unavailable))).toBe(false);
    expect(isDaemonConnectingError(new Error(PENDING_DAEMON_MESSAGE))).toBe(false);
  });
});

describe("end to end: the pending signal renders as a wait state, not a raw error", () => {
  it("classifyDaemonWait produces a non-failed, retryable state for a fresh wait", () => {
    // A surface that catches the pending error and flips into "waiting on
    // daemon" mode (as FileTree.tsx does via isDaemonConnectingError) hands
    // off to classifyDaemonWait with no daemon record yet (elapsedMs=0).
    const state = classifyDaemonWait({ elapsedMs: 0, isCloud: true });

    expect(state.tone).not.toBe("failed");
    expect(state.shouldRetry).toBe(true);
    // Never renders the raw backend message as the headline — that was the
    // reported bug (the raw "[internal] resolving daemon..." string shown
    // verbatim to the user).
    expect(state.title).not.toContain("resolving daemon");
    expect(state.title).not.toContain("no daemon connected");
  });
});

describe("isNoMachineError", () => {
  it("recognises toolexec.ErrNoDaemon — no machine at all — and nothing that is a wait", () => {
    const none = withReason(
      "resolving daemon for command: no daemon available: no machine is connected to your account yet",
      Code.FailedPrecondition,
      "no_daemon",
    );
    expect(isNoMachineError(none)).toBe(true);
    expect(isDaemonConnectingError(none)).toBe(false);

    expect(isNoMachineError(withReason(PENDING_DAEMON_MESSAGE, Code.Unavailable, "daemon_starting"))).toBe(false);
    expect(isNoMachineError(withReason("no daemon connected for user", Code.Unavailable, "daemon_not_connected"))).toBe(false);
  });
});
