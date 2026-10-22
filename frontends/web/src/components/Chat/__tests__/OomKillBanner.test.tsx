// Copyright (c) 2025 Reliant Labs

/**
 * The OOM banner also says when the kernel killed a single process at the
 * machine's memory limit. Inside a kata VM such a kill restarts nothing, so the
 * operator never records it; it reaches the app only on the daemon's
 * heartbeat. Regression for 2026-10-19, when builds were killed at a 64 GiB
 * limit and the UI said nothing.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const state = vi.hoisted(() => ({ daemons: [] as unknown[], activeId: "" }));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ activeDaemon: state.activeId ? { daemonId: state.activeId } : undefined }),
  invalidateDaemonList: vi.fn(),
  reconcileDaemonHeartbeat: vi.fn(),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));

import { OomKillBanner } from "../OomKillBanner";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";

const GiB = 2 ** 30;

function reportKills(daemonId: string, count: number, lastAtMs: number) {
  act(() => {
    useGlobalUpdatesStore.setState({
      daemonProcessOomKills: { [daemonId]: { count, lastAtMs, limitBytes: 64 * GiB } },
    });
  });
}

describe("OomKillBanner: process kills from the heartbeat", () => {
  beforeEach(() => {
    sessionStorage.clear();
    state.daemons = [];
    state.activeId = "d-1";
    useGlobalUpdatesStore.setState({ daemonProcessOomKills: {} });
  });
  afterEach(cleanup);

  it("says a process was killed at the limit", () => {
    reportKills("d-1", 2, Date.now() - 60_000);
    render(<OomKillBanner />);
    expect(screen.getByTestId("oom-kill-banner-process").textContent).toContain(
      "A process on your machine was killed: the machine hit its 64 GiB memory limit (2 times since the machine started).",
    );
  });

  it("stays quiet for an old kill, or a kill on another machine", () => {
    reportKills("d-1", 1, Date.now() - 2 * 60 * 60 * 1000);
    const { unmount } = render(<OomKillBanner />);
    expect(screen.queryByTestId("oom-kill-banner-process")).toBeNull();
    unmount();

    reportKills("d-2", 1, Date.now());
    render(<OomKillBanner />);
    expect(screen.queryByTestId("oom-kill-banner-process")).toBeNull();
  });

  it("comes back for a newer kill after a dismissal", async () => {
    const first = Date.now() - 60_000;
    reportKills("d-1", 1, first);
    render(<OomKillBanner />);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("oom-kill-banner-process")).toBeNull();

    reportKills("d-1", 2, first + 30_000);
    expect(screen.getByTestId("oom-kill-banner-process")).toBeTruthy();
  });
});
