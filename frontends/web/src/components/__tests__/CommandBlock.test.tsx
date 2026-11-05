import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CommandBlock } from "../CommandBlock";

const writeText = vi.fn();

beforeEach(() => {
  writeText.mockReset().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
});

afterEach(() => vi.useRealTimers());

describe("CommandBlock", () => {
  it("shows the command and an always-visible, labelled copy button", () => {
    render(<CommandBlock command="reliant auth serve" label="sign-in helper command" />);

    expect(screen.getByText("reliant auth serve")).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Copy sign-in helper command" });
    // Never gated on hover: no opacity-0 / group-hover reveal.
    expect(button.className).not.toMatch(/opacity-0|group-hover/);
    expect(button).toHaveTextContent("Copy");
  });

  it("copies the exact command, shows Copied, and announces it politely", async () => {
    render(<CommandBlock command="brew install --cask x" label="Homebrew install command" />);

    fireEvent.click(screen.getByRole("button", { name: /Copy Homebrew install command/i }));

    await waitFor(() => expect(screen.getByRole("button")).toHaveTextContent("Copied"));
    expect(writeText).toHaveBeenCalledWith("brew install --cask x");
    const live = screen.getByRole("status");
    expect(live).toHaveAttribute("aria-live", "polite");
    expect(live).toHaveTextContent("Copied Homebrew install command to clipboard");
  });

  it("returns to Copy after a moment", async () => {
    vi.useFakeTimers();
    render(<CommandBlock command="x" />);

    fireEvent.click(screen.getByRole("button"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByRole("button")).toHaveTextContent("Copied");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    expect(screen.getByRole("button")).toHaveTextContent("Copy");
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("says so, rather than claiming success, when the clipboard is refused", async () => {
    writeText.mockRejectedValue(new Error("denied"));
    render(<CommandBlock command="x" label="command" />);

    fireEvent.click(screen.getByRole("button"));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(/Couldn't copy/));
    expect(screen.getByRole("button")).not.toHaveTextContent("Copied");
  });

  it("is reachable by keyboard: a native button, in tab order", () => {
    render(<CommandBlock command="x" />);
    const button = screen.getByRole("button");
    expect(button.tagName).toBe("BUTTON");
    expect(button).not.toHaveAttribute("tabindex", "-1");
  });
});
