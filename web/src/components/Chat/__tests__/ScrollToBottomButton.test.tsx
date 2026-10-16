import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ScrollToBottomButton } from "../ScrollToBottomButton";

function button(): HTMLElement {
  return screen.getByRole("button", { name: /scroll to bottom/i });
}

afterEach(() => {
  vi.useRealTimers();
});

describe("ScrollToBottomButton", () => {
  // It used to hang off the composer's top edge by a fixed -44px, which put it
  // squarely on whatever sat between the transcript and the composer — the
  // background-work pill above all. Anchored to the bottom of its positioned
  // container (the transcript frame), that band can only ever push it up.
  it("anchors to the bottom edge of its container instead of offsetting from the composer", () => {
    render(<ScrollToBottomButton visible onClick={() => {}} />);

    expect(button().parentElement).toHaveClass("absolute", "bottom-3");
    expect(button().style.marginTop).toBe("");
  });

  it("keeps the overlay from intercepting clicks meant for the transcript", () => {
    render(<ScrollToBottomButton visible onClick={() => {}} />);

    expect(button().parentElement).toHaveClass("pointer-events-none");
    expect(button()).toHaveClass("pointer-events-auto");
  });

  it("stays clearly visible after it fades at idle", () => {
    vi.useFakeTimers();
    render(<ScrollToBottomButton visible onClick={() => {}} />);

    act(() => {
      vi.advanceTimersByTime(2500);
    });

    // Faded, but still a button you can see: at 15% it read as a smudge.
    expect(button()).toHaveClass("opacity-60");
    expect(button()).not.toHaveClass("opacity-15");
  });

  it("returns to full opacity on hover", () => {
    vi.useFakeTimers();
    render(<ScrollToBottomButton visible onClick={() => {}} />);

    act(() => {
      vi.advanceTimersByTime(2500);
    });
    fireEvent.mouseEnter(button());

    expect(button()).toHaveClass("opacity-100");
  });
});

it("uses a fixed touch target and announces unread content on mobile", () => {
  vi.useFakeTimers();
  render(<SurfaceProvider surface="mobile"><ScrollToBottomButton visible hasNewContent onClick={() => {}} /></SurfaceProvider>);
  act(() => vi.advanceTimersByTime(180));
  expect(screen.getByRole("button", {name:"Scroll to bottom, new messages"})).toHaveClass("h-[44px]", "w-[44px]");
  expect(screen.queryByText("Scroll to bottom")).not.toBeInTheDocument();
});

it("does not flash the mobile arrow during a temporary measurement change", () => {
  vi.useFakeTimers();
  const { rerender } = render(<SurfaceProvider surface="mobile"><ScrollToBottomButton visible onClick={() => {}} /></SurfaceProvider>);
  act(() => vi.advanceTimersByTime(100));
  rerender(<SurfaceProvider surface="mobile"><ScrollToBottomButton visible={false} onClick={() => {}} /></SurfaceProvider>);
  act(() => vi.advanceTimersByTime(300));
  expect(screen.queryByRole("button", { name: /scroll to bottom/i })).toBeNull();
});
