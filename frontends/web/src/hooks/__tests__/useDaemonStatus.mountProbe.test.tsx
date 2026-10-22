import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// The renderer RELOADS as part of the post-sign-in daemon restart, and the
// main process de-duplicates `daemon-connected` on the stream value. So the
// event fires at the OUTGOING renderer, and the freshly-mounted one — the one
// actually showing the onboarding compute step — receives nothing at all.
//
// Measured on a real prod sign-in: the daemon became listable at 22:20:11.233,
// the UI only learned at 22:20:15.289 on the next 5s poll tick, and zero
// events were delivered in between. A renderer therefore ASKS on mount instead
// of relying on having been listening at the right moment.
// ---------------------------------------------------------------------------

const listDaemonsMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: listDaemonsMock }) },
}));

import { useDaemonStatus } from "../useDaemonStatus";

/** Must match the key inside useDaemonStatus. */
const DAEMON_LIST_QUERY_KEY = ["reliant", "daemonRegistry", "list"] as const;

function Reader() {
  const { daemons } = useDaemonStatus();
  return React.createElement("div", null, String(daemons.length));
}

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) =>
    React.createElement(QueryClientProvider, { client }, children);
  return { Wrapper, client };
}

/**
 * Install a window.electronAPI whose `daemon-connected` event never fires —
 * which is exactly the reload case. If the hook only listened, it would be
 * left waiting for the poll.
 */
function installElectronAPI({ connected }: { connected: boolean }) {
  const isDaemonConnected = vi.fn().mockResolvedValue(connected);
  const unsubscribe = vi.fn();
  const onDaemonConnected = vi.fn().mockReturnValue(unsubscribe);
  (window as unknown as { electronAPI?: unknown }).electronAPI = {
    isDaemonConnected,
    onDaemonConnected,
  };
  return { isDaemonConnected, onDaemonConnected, unsubscribe };
}

beforeEach(() => {
  listDaemonsMock.mockReset();
  listDaemonsMock.mockResolvedValue({ daemons: [] });
});

afterEach(() => {
  cleanup();
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

describe("useDaemonStatus mount probe", () => {
  it("refetches a CACHED list when the daemon is already connected", async () => {
    // The case the probe actually exists for. staleTime is the poll interval,
    // so a renderer that mounts with a warm cache issues no fetch of its own —
    // it would sit on a list captured before the daemon registered until the
    // next 5s tick. The probe turns that into an immediate refetch.
    //
    // On a COLD mount the probe is deliberately a no-op: the mount fetch is
    // already in flight and React Query coalesces the invalidation into it,
    // which is why this test seeds the cache rather than asserting two
    // fetches from a fresh render.
    const { isDaemonConnected } = installElectronAPI({ connected: true });
    const { Wrapper, client } = makeWrapper();
    client.setQueryData(DAEMON_LIST_QUERY_KEY, []);

    render(React.createElement(Wrapper, null, React.createElement(Reader)));

    await waitFor(() => expect(isDaemonConnected).toHaveBeenCalled());
    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));
  });

  it("does not refetch a cached list when the daemon is not yet connected", async () => {
    const { isDaemonConnected } = installElectronAPI({ connected: false });
    const { Wrapper, client } = makeWrapper();
    client.setQueryData(DAEMON_LIST_QUERY_KEY, []);

    render(React.createElement(Wrapper, null, React.createElement(Reader)));

    await waitFor(() => expect(isDaemonConnected).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 100));
    expect(listDaemonsMock).not.toHaveBeenCalled();
  });

  it("still subscribes to the event for a renderer that is already up", async () => {
    const { onDaemonConnected } = installElectronAPI({ connected: false });
    const { Wrapper } = makeWrapper();

    render(React.createElement(Wrapper, null, React.createElement(Reader)));

    await waitFor(() => expect(onDaemonConnected).toHaveBeenCalledTimes(1));
  });

  it("survives a host that exposes no probe (web, not Electron)", async () => {
    const { Wrapper } = makeWrapper();

    render(React.createElement(Wrapper, null, React.createElement(Reader)));

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });
});
