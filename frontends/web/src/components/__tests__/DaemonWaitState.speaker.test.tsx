import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";

// ---------------------------------------------------------------------------
// When the machine is down, the chat composer, the file tree and the terminal
// are all blocked at once, and each renders a DaemonWaitState. In full, that
// was the same headline, paragraph and pair of buttons three times over — and
// three live regions announcing it. These tests pin the contract that replaced
// it: every blocked surface still names the wait, but exactly one visible
// surface explains it and offers the exits, and its retry reaches the rest.
// ---------------------------------------------------------------------------

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

import { DaemonWaitState } from "../DaemonWaitState";
import type { DaemonWaitState as WaitState } from "../../lib/daemon-wait";

/** The escalated cloud wait from the bug report: copy plus both exits. */
const STUCK: WaitState = {
  tone: "slow",
  title: "Preparing your machine",
  detail: "Your compute is taking a while to come up.",
  reason: null,
  shouldRetry: true,
  showRetry: true,
  showManage: true,
};

/** Controllable IntersectionObserver: tests decide what is on screen. */
class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  target: Element | null = null;
  constructor(private readonly callback: IntersectionObserverCallback) {
    FakeIntersectionObserver.instances.push(this);
  }
  observe(target: Element) {
    this.target = target;
  }
  unobserve() {}
  disconnect() {
    this.target = null;
  }
  takeRecords() {
    return [];
  }
  report(isIntersecting: boolean) {
    if (!this.target) return;
    this.callback(
      [{ isIntersecting, target: this.target } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
  static forTestId(testId: string) {
    return FakeIntersectionObserver.instances.find(
      (io) => io.target?.closest(`[data-testid="${testId}"]`) != null,
    );
  }
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  FakeIntersectionObserver.instances = [];
});

describe("DaemonWaitState with several blocked surfaces", () => {
  it("explains the wait once, from the chat composer, while the others echo the headline", () => {
    render(
      <>
        <div data-testid="chat">
          <DaemonWaitState state={STUCK} variant="inline" onRetry={() => {}} />
        </div>
        <div data-testid="files">
          <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={() => {}} />
        </div>
        <div data-testid="terminal">
          <DaemonWaitState state={STUCK} variant="overlay" secondary onRetry={() => {}} />
        </div>
      </>,
    );

    // Every blocked surface still says why it's empty...
    expect(screen.getAllByText("Preparing your machine")).toHaveLength(3);
    // ...but only one explains it and offers the exits.
    expect(screen.getAllByText(STUCK.detail!)).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: /Try again/ })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: /Manage machines/ })).toHaveLength(1);
    expect(within(screen.getByTestId("chat")).getByRole("button", { name: /Try again/ })).toBeTruthy();
    // One announcement, not three.
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("lets the file tree speak when no primary surface is up, and keeps the terminal as an echo", () => {
    render(
      <>
        <div data-testid="files">
          <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={() => {}} />
        </div>
        <div data-testid="terminal">
          <DaemonWaitState state={STUCK} variant="overlay" secondary onRetry={() => {}} />
        </div>
      </>,
    );

    expect(within(screen.getByTestId("files")).getByText(STUCK.detail!)).toBeTruthy();
    expect(within(screen.getByTestId("terminal")).queryByText(STUCK.detail!)).toBeNull();
    expect(within(screen.getByTestId("terminal")).getByText("Preparing your machine")).toBeTruthy();
  });

  it("hands the message to the next surface when the speaker goes away", () => {
    const view = render(
      <>
        <div data-testid="first">
          <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={() => {}} />
        </div>
        <div data-testid="second">
          <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={() => {}} />
        </div>
      </>,
    );

    // Ties go to the first to mount, so the speaker doesn't hop around.
    expect(within(screen.getByTestId("first")).getByText(STUCK.detail!)).toBeTruthy();
    expect(within(screen.getByTestId("second")).queryByText(STUCK.detail!)).toBeNull();

    view.rerender(
      <div data-testid="second">
        <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={() => {}} />
      </div>,
    );

    expect(within(screen.getByTestId("second")).getByText(STUCK.detail!)).toBeTruthy();
  });

  it("never elects a surface nobody can see", () => {
    // Terminals stay mounted while hidden to keep their shells alive. A hidden
    // one must not take the message from a visible one.
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    render(
      <>
        <div data-testid="hidden-terminal">
          <DaemonWaitState state={STUCK} variant="overlay" secondary onRetry={() => {}} />
        </div>
        <div data-testid="visible-terminal">
          <DaemonWaitState state={STUCK} variant="overlay" secondary onRetry={() => {}} />
        </div>
      </>,
    );
    expect(within(screen.getByTestId("hidden-terminal")).getByText(STUCK.detail!)).toBeTruthy();

    act(() => {
      FakeIntersectionObserver.forTestId("hidden-terminal")?.report(false);
      FakeIntersectionObserver.forTestId("visible-terminal")?.report(true);
    });

    expect(within(screen.getByTestId("hidden-terminal")).queryByText(STUCK.detail!)).toBeNull();
    expect(within(screen.getByTestId("visible-terminal")).getByText(STUCK.detail!)).toBeTruthy();
  });

  it("retries every deferring surface from the one Try again button", () => {
    const chatRetry = vi.fn();
    const filesRetry = vi.fn();
    const terminalRetry = vi.fn();
    render(
      <>
        <DaemonWaitState state={STUCK} variant="inline" onRetry={chatRetry} />
        <DaemonWaitState state={STUCK} variant="panel" secondary onRetry={filesRetry} />
        <DaemonWaitState state={STUCK} variant="overlay" secondary onRetry={terminalRetry} />
      </>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Try again/ }));

    // The deferring surfaces have no button of their own any more; without
    // the fan-out, a file tree on a resumed machine would stay stuck.
    expect(chatRetry).toHaveBeenCalledTimes(1);
    expect(filesRetry).toHaveBeenCalledTimes(1);
    expect(terminalRetry).toHaveBeenCalledTimes(1);
  });
});
