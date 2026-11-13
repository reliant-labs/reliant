import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

import { CONNECT_WAIT_SLOW_MS, ConnectWait, connectionFacts } from "../ConnectWait";

/**
 * A user on Windows waited 4m36s on a bare spinner for a local machine that
 * never connected (replay aeb5e203…). The wait now shows progress from the
 * first second and, once it is slow, offers a retry, a plain read of what the
 * app can see, and a way past — without ever blocking the connection itself.
 */

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  vi.useRealTimers();
  delete (window as unknown as { electronAPI?: unknown }).electronAPI;
});

const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

describe("ConnectWait", () => {
  it("shows what it is waiting for and a running counter", async () => {
    render(<ConnectWait label="Waiting for your computer to connect" onRetry={vi.fn()} daemons={[]} />);
    expect(screen.getByText(/waiting for your computer to connect/i)).toBeInTheDocument();
    expect(screen.getByTestId("connect-wait-elapsed")).toHaveTextContent("0s");

    await tick(7_000);
    expect(screen.getByTestId("connect-wait-elapsed")).toHaveTextContent("7s");
  });

  it("offers nothing extra while the wait is still normal", async () => {
    render(<ConnectWait label="Waiting" onRetry={vi.fn()} daemons={[]} />);
    await tick(CONNECT_WAIT_SLOW_MS - 1_000);

    expect(screen.queryByTestId("connect-wait-slow")).toBeNull();
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  it("after ~30s offers retry, an explanation, and a way past", async () => {
    const onRetry = vi.fn();
    const onSkip = vi.fn();
    render(
      <ConnectWait label="Waiting" onRetry={onRetry} daemons={[]} skip={{ label: "Choose a machine myself", onClick: onSkip }} />,
    );
    await tick(CONNECT_WAIT_SLOW_MS);

    expect(screen.getByText(/taking longer than usual/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: /choose a machine myself/i }));
    expect(onSkip).toHaveBeenCalledTimes(1);
  });

  it("'What's happening?' lists what the app can see", async () => {
    render(<ConnectWait label="Waiting" onRetry={vi.fn()} daemons={[{ status: DaemonStatus.PENDING }]} />);
    await tick(CONNECT_WAIT_SLOW_MS);
    expect(screen.queryByTestId("connect-wait-facts")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /what's happening/i }));
    const facts = screen.getByTestId("connect-wait-facts");
    expect(facts).toHaveTextContent(/1 \(starting\)/);
    expect(facts).toHaveTextContent(/servers: reachable/i);
  });

  it("in the desktop app, asks whether the built-in machine is connected", async () => {
    const isDaemonConnected = vi.fn().mockResolvedValue(false);
    (window as unknown as { electronAPI?: unknown }).electronAPI = { isDaemonConnected };
    render(<ConnectWait label="Waiting" onRetry={vi.fn()} daemons={[]} />);
    await tick(CONNECT_WAIT_SLOW_MS);
    fireEvent.click(screen.getByRole("button", { name: /what's happening/i }));

    expect(isDaemonConnected).toHaveBeenCalled();
    expect(screen.getByTestId("connect-wait-facts")).toHaveTextContent(/built-in machine: not connected/i);
  });
});

describe("connectionFacts", () => {
  it("says when the registry cannot be reached", () => {
    const facts = connectionFacts({ machines: [], unreachable: true, desktop: false, bundledDaemonConnected: null });
    expect(facts[0]).toMatch(/not answering/i);
    expect(facts[1]).toMatch(/none yet/i);
  });

  it("names the state of every machine", () => {
    const facts = connectionFacts({
      machines: [{ status: DaemonStatus.ACTIVE }, { status: DaemonStatus.SUSPENDED }],
      unreachable: false,
      desktop: false,
      bundledDaemonConnected: null,
    });
    expect(facts.join("\n")).toMatch(/2 \(connected, asleep\)/);
  });
});
