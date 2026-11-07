import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// The desktop app's startup, and every workspace restore, sent a burst of
// ListDaemons that the client aborted itself (dev log, Electron renderer):
//
//   [gRPC Client] Request aborted {"method":"ListDaemons","durationMs":0,
//     "errorMessage":"signal is aborted without reason","errorCode":20}
//   … ten of those in one millisecond, then one `rpc completed` on the server.
//
// Every mounted useDaemonStatus (two dozen call sites) ran its own Electron
// probe and its own `daemon-connected` listener, and each one invalidated the
// list. invalidateQueries cancels the fetch in flight and starts another, so N
// readers turned one "the daemon is connected" into N requests, N-1 of them
// aborted the moment the next one started.
//
// These tests count requests at the transport — an aborted request is still a
// request — with a response slow enough that the burst overlaps it, as the
// real ~200ms one does.
// ---------------------------------------------------------------------------

const listDaemonsMock = vi.hoisted(() => vi.fn());

import { useDaemonStatus } from "../useDaemonStatus";
import { DAEMON_LIST_QUERY_KEY, invalidateDaemonList } from "../daemon-registry-queries";
import { setTransport } from "@/lib/connect";
import { daemonRegistryTransport, seedDaemonList } from "@/test/daemonRegistry";

const RESPONSE_MS = 30;

beforeEach(() => {
  listDaemonsMock.mockReset();
  listDaemonsMock.mockImplementation(
    () => new Promise((resolve) => setTimeout(() => resolve({ daemons: [] }), RESPONSE_MS)),
  );
  setTransport(daemonRegistryTransport({ listDaemons: () => listDaemonsMock() }));
});

afterEach(() => {
  cleanup();
  setTransport(null);
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

function Reader() {
  const { daemons } = useDaemonStatus();
  return React.createElement("div", null, String(daemons.length));
}

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const Wrapper = ({ children }: { children: ReactNode }) =>
    React.createElement(QueryClientProvider, { client }, children);
  return { Wrapper, client };
}

function renderReaders(Wrapper: (props: { children: ReactNode }) => React.ReactElement, count: number) {
  return render(
    React.createElement(
      Wrapper,
      null,
      ...Array.from({ length: count }, (_, i) => React.createElement(Reader, { key: i })),
    ),
  );
}

/** A desktop host whose daemon is already connected; `fire` emits the event. */
function installElectronAPI() {
  const listeners = new Set<(payload: unknown) => void>();
  const api = {
    isDaemonConnected: vi.fn().mockResolvedValue(true),
    onDaemonConnected: vi.fn((cb: (payload: unknown) => void) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    }),
  };
  (window as unknown as { electronAPI?: unknown }).electronAPI = api;
  return { api, fire: () => listeners.forEach((cb) => cb({})), listenerCount: () => listeners.size };
}

const settle = () => new Promise((r) => setTimeout(r, RESPONSE_MS * 4));

describe("daemon list request bursts", () => {
  it("ten readers mounting on a warm cache ask the desktop app once and refetch once", async () => {
    // The workspace-restore shape: the list is cached, the readers remount,
    // and the daemon is connected. One probe, one request.
    const { api } = installElectronAPI();
    const { Wrapper, client } = makeWrapper();
    seedDaemonList(client);

    renderReaders(Wrapper, 10);

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalled());
    await settle();
    expect(api.isDaemonConnected).toHaveBeenCalledTimes(1);
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("ten readers on a cold start issue one request", async () => {
    installElectronAPI();
    const { Wrapper } = makeWrapper();

    renderReaders(Wrapper, 10);

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalled());
    await settle();
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("one daemon-connected event refetches once, however many readers are mounted", async () => {
    const { api, fire, listenerCount } = installElectronAPI();
    api.isDaemonConnected.mockResolvedValue(false);
    const { Wrapper, client } = makeWrapper();
    seedDaemonList(client);

    renderReaders(Wrapper, 10);
    await waitFor(() => expect(api.onDaemonConnected).toHaveBeenCalled());
    expect(listenerCount()).toBe(1);

    act(() => fire());

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalled());
    await settle();
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("stops listening when the last reader unmounts", async () => {
    const { api, listenerCount } = installElectronAPI();
    api.isDaemonConnected.mockResolvedValue(false);
    const { Wrapper } = makeWrapper();

    const view = renderReaders(Wrapper, 3);
    await waitFor(() => expect(listenerCount()).toBe(1));

    view.unmount();
    expect(listenerCount()).toBe(0);
  });

  it("a synchronous burst of invalidations is one request, not N-1 aborted ones", async () => {
    const { Wrapper, client } = makeWrapper();
    seedDaemonList(client);
    renderReaders(Wrapper, 1);

    for (let i = 0; i < 10; i++) invalidateDaemonList(client);

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalled());
    await settle();
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("a signal arriving mid-fetch is answered by one more fetch that starts after it", async () => {
    // Freshness: the fetch in flight may predate the change the signal
    // announces, so its answer cannot settle the signal. Rather than abort it,
    // one follow-up request runs once it lands.
    const { Wrapper, client } = makeWrapper();
    seedDaemonList(client);
    renderReaders(Wrapper, 1);

    invalidateDaemonList(client);
    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));
    const firstStartedAt = Date.now();

    // Three signals while the first request is in flight.
    invalidateDaemonList(client);
    invalidateDaemonList(client);
    invalidateDaemonList(client);
    // Marked stale at once, so a reader mounting now does not trust it.
    expect(client.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated).toBe(true);

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(2));
    expect(Date.now() - firstStartedAt).toBeGreaterThanOrEqual(RESPONSE_MS - 5);
    await settle();
    expect(listDaemonsMock).toHaveBeenCalledTimes(2);
  });
});
