import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CopyButton } from "../tool-renderers/CopyButton";

const writeText = vi.fn<(...args: [string]) => Promise<void>>();

beforeEach(() => {
  writeText.mockReset().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
});

afterEach(() => {
  cleanup();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function copy() {
  await act(async () => {
    fireEvent.click(screen.getByRole("button"));
  });
}

describe("CopyButton timer ownership", () => {
  it("cancels the pending reset on unmount", async () => {
    vi.useFakeTimers();
    const view = render(<CopyButton content="output" />);
    await copy();
    expect(screen.getByRole("button", { name: "Copied!" })).toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(1);

    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
    await act(async () => { await vi.runAllTimersAsync(); });
  });

  it("restarts the single reset timer after another copy", async () => {
    vi.useFakeTimers();
    render(<CopyButton content="output" />);
    await copy();
    act(() => vi.advanceTimersByTime(1000));
    await copy();
    expect(vi.getTimerCount()).toBe(1);

    act(() => vi.advanceTimersByTime(1000));
    expect(screen.getByRole("button", { name: "Copied!" })).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1000));
    expect(screen.getByRole("button", { name: "Copy to clipboard" })).toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(0);
    expect(writeText).toHaveBeenCalledTimes(2);
  });

  it("still resets after two seconds under StrictMode effect replay", async () => {
    vi.useFakeTimers();
    render(<StrictMode><CopyButton content="output" /></StrictMode>);
    await copy();
    expect(writeText).toHaveBeenCalledWith("output");
    act(() => vi.advanceTimersByTime(1999));
    expect(screen.getByRole("button", { name: "Copied!" })).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.getByRole("button", { name: "Copy to clipboard" })).toBeInTheDocument();
  });

  it("does not schedule a reset when clipboard completion arrives after unmount", async () => {
    vi.useFakeTimers();
    let finish!: () => void;
    writeText.mockReturnValue(new Promise<void>((resolve) => { finish = resolve; }));
    const view = render(<CopyButton content="output" />);
    await copy();
    view.unmount();
    await act(async () => { finish(); });
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not copy or schedule a reset when content resolves after unmount", async () => {
    vi.useFakeTimers();
    let finish!: (value: string) => void;
    const resolveContent = () => new Promise<string>((resolve) => { finish = resolve; });
    const view = render(<CopyButton content="preview" resolveContent={resolveContent} />);
    await copy();
    view.unmount();
    await act(async () => { finish("full output"); });
    expect(writeText).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("never invokes the canceled reset callback with real timers", async () => {
    // Shorten only the component's two-second reset; keep the native timer
    // implementation so this proves cancellation, not just a mounted guard.
    const nativeSetTimeout = globalThis.setTimeout;
    const resetCallback = vi.fn();
    const scheduledReset = vi.fn();
    vi.spyOn(globalThis, "setTimeout").mockImplementation(((...timerArgs: Parameters<typeof setTimeout>) => {
      const [callback, delay, ...args] = timerArgs;
      if (delay === 2000 && typeof callback === "function") {
        scheduledReset();
        return nativeSetTimeout(() => { resetCallback(); callback(...args); }, 1000);
      }
      return nativeSetTimeout(callback, delay, ...args);
    }) as typeof setTimeout);

    const view = render(<CopyButton content="output" />);
    await copy();
    expect(screen.getByRole("button", { name: "Copied!" })).toBeInTheDocument();
    view.unmount();
    expect(scheduledReset).toHaveBeenCalledTimes(1);
    await new Promise<void>((resolve) => nativeSetTimeout(resolve, 1100));
    expect(resetCallback).not.toHaveBeenCalled();
  });
});
