import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { render, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// ---------------------------------------------------------------------------
// A dozen surfaces read the daemon list — ModernApp, NewChatView, Terminal,
// DaemonStatusDot, TabbedViewerPanel, DetectedPortsChip, OomKillBanner,
// ProjectPicker, ConnectDaemonModal, WorkspacesSection, ComputeStep,
// SelfHostedDaemonConnect. They share one query key, so consumers that mount
// together already share a request.
//
// Consumers that mount APART did not. With staleTime 0 the cache is stale the
// instant it is written, so each surface appearing as the user navigated found
// stale data and issued its own fetch, on top of the 5s poll that was already
// keeping the list fresh.
//
// staleTime now matches the poll interval, which closes that without loosening
// the freshness bound: the poll still refetches on its own schedule, so the
// data is never older than one interval either way.
// ---------------------------------------------------------------------------

const listDaemonsMock = vi.hoisted(() => vi.fn());

vi.mock("../../api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons: listDaemonsMock }) },
}));

import { useDaemonStatus } from "../useDaemonStatus";

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

beforeEach(() => {
  listDaemonsMock.mockReset();
  listDaemonsMock.mockResolvedValue({ daemons: [] });
});

afterEach(() => {
  cleanup();
});

describe("useDaemonStatus request fan-out", () => {
  it("ten consumers mounting together issue a single request", async () => {
    const { Wrapper } = makeWrapper();

    render(
      React.createElement(
        Wrapper,
        null,
        ...Array.from({ length: 10 }, (_, i) =>
          React.createElement(Reader, { key: i }),
        ),
      ),
    );

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("consumers mounting later reuse the cached list instead of refetching", async () => {
    const { Wrapper } = makeWrapper();

    render(React.createElement(Wrapper, null, React.createElement(Reader)));
    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));

    // Five more surfaces appear as the user navigates, all well inside the
    // poll window. None of them should hit the wire.
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 20));
      render(React.createElement(Wrapper, null, React.createElement(Reader)));
    }

    await new Promise((r) => setTimeout(r, 100));
    expect(listDaemonsMock).toHaveBeenCalledTimes(1);
  });

  it("refresh() still forces a fetch despite the staleTime", async () => {
    const { Wrapper } = makeWrapper();

    let refresh: (() => void) | undefined;
    function RefreshReader() {
      const status = useDaemonStatus();
      refresh = status.refresh;
      return null;
    }

    render(
      React.createElement(Wrapper, null, React.createElement(RefreshReader)),
    );
    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(1));

    refresh?.();

    await waitFor(() => expect(listDaemonsMock).toHaveBeenCalledTimes(2));
  });
});
