/**
 * The returning-user heal must be INERT when sessionStorage throws.
 *
 * The heal writes `CompleteOnboarding` — server state — from a `useEffect`,
 * with no user action. Its one defence against ending the flow the user is
 * actively in is a sessionStorage mark: "we watched this user start from
 * nothing in this session, so the daemon that appeared since is this flow's
 * own work, not evidence of a past completion." Without that mark the heal
 * sees onboarding's own freshly-provisioned daemon and declares the user
 * finished 21ms after creating it.
 *
 * sessionStorage is not always available. It throws in Safari private mode and
 * in sandboxed iframes, and in packaged Electron the renderer runs on
 * `app://bundle`, an origin whose storage partitioning has already caused one
 * bug in this repo (`3fcd9f79`). When the write throws, the mark is silently
 * lost and the hook falls back to daemon evidence alone — which is the
 * pre-`63b18468` behaviour that shipped the bug.
 *
 * So: if storage is broken, the heal must decline to fire rather than fire on
 * incomplete information. Ending a flow wrongly is unrecoverable for the user;
 * declining to heal costs them one extra click through onboarding.
 */
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";
import { useReturningUserHeal } from "../useReturningUserHeal";

const USER_CREATED_MS = Date.UTC(2026, 0, 10, 0, 0, 0);

/** A daemon created after the account — the shape that arms the heal. */
function daemon(createdAtMs = USER_CREATED_MS + 60_000) {
  return {
    status: DaemonStatus.ACTIVE,
    createdAt: { seconds: BigInt(Math.floor(createdAtMs / 1000)) },
  };
}

function Harness({ onHeal, daemons }: { onHeal: () => void; daemons: ReturnType<typeof daemon>[] }) {
  useReturningUserHeal({
    userId: "user-1",
    userCreatedAtMs: USER_CREATED_MS,
    daemons,
    daemonsLoading: false,
    isComplete: false,
    onHeal,
  });
  return null;
}

/**
 * Break one sessionStorage method for the duration of a test.
 *
 * Spying on `Storage.prototype` is not enough: jsdom's `sessionStorage` is an
 * exotic Proxy-backed object whose own methods shadow the prototype, so a
 * prototype override is never consulted and the test passes without ever
 * exercising the failure. Stubbing the global with an object that throws is
 * what actually reproduces private mode.
 */
function breakStorage(method: "getItem" | "setItem") {
  const throwing = () => {
    throw new DOMException(`${method} unavailable`);
  };
  vi.stubGlobal("sessionStorage", {
    getItem: method === "getItem" ? throwing : () => null,
    setItem: method === "setItem" ? throwing : () => undefined,
    removeItem: () => undefined,
    clear: () => undefined,
    key: () => null,
    length: 0,
  });
}

beforeEach(() => {
  sessionStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("useReturningUserHeal — storage unavailable", () => {
  it("does not heal when sessionStorage.setItem throws", () => {
    // The mid-flow user: no daemon on the first render, so the hook tries to
    // record "seen in progress" — and the write throws.
    breakStorage("setItem");

    const onHeal = vi.fn();
    const { rerender } = render(<Harness onHeal={onHeal} daemons={[]} />);
    expect(onHeal).not.toHaveBeenCalled();

    // Onboarding provisions a machine mid-flow. With the mark lost, the naive
    // check sees this daemon and calls the user finished.
    rerender(<Harness onHeal={onHeal} daemons={[daemon()]} />);

    expect(onHeal).not.toHaveBeenCalled();
  });

  it("does not heal when sessionStorage.getItem throws", () => {
    breakStorage("getItem");

    const onHeal = vi.fn();
    render(<Harness onHeal={onHeal} daemons={[daemon()]} />);

    expect(onHeal).not.toHaveBeenCalled();
  });

  it("still heals a genuine returning user when storage works", () => {
    // Guards the guard: proves the assertions above fail for the right reason
    // (storage broken) rather than because the heal never fires at all.
    const onHeal = vi.fn();
    render(<Harness onHeal={onHeal} daemons={[daemon()]} />);

    expect(onHeal).toHaveBeenCalledTimes(1);
  });
});
