/**
 * The desktop find bar's page, against a stand-in for its preload API: what
 * it asks the main process for as the user types, steps and closes, and what
 * it shows of the results main reports back.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { FindBar } from "../FindBar";
import { applyTheme, themeSnapshot, type FindBarAPI, type FindBarOpened, type FindResult } from "../protocol";

function fakeApi() {
  let opened: ((event: FindBarOpened) => void) | undefined;
  let result: ((result: FindResult) => void) | undefined;
  const api = {
    ready: vi.fn(),
    find: vi.fn(),
    close: vi.fn(),
    onOpened: vi.fn((callback: (event: FindBarOpened) => void) => {
      opened = callback;
      return () => (opened = undefined);
    }),
    onResult: vi.fn((callback: (result: FindResult) => void) => {
      result = callback;
      return () => (result = undefined);
    }),
  } satisfies FindBarAPI;
  return {
    api,
    open: (theme = { dark: true, colorScheme: "pure-black" }) => act(() => opened?.({ theme })),
    report: (matches: number, activeMatchOrdinal: number) =>
      act(() => result?.({ matches, activeMatchOrdinal, finalUpdate: true })),
    subscribed: () => opened !== undefined && result !== undefined,
  };
}

function setup({ isMac = true } = {}) {
  const bar = fakeApi();
  const view = render(<FindBar api={bar.api} isMac={isMac} />);
  const input = screen.getByRole("textbox", { name: "Find in page" });
  return { ...bar, view, input: input as HTMLInputElement };
}

const newSearch = (text: string) => ({ text, forward: true, findNext: true, matchCase: false });
const stepTo = (text: string, forward: boolean) => ({ text, forward, findNext: false, matchCase: false });
const count = () => screen.getByTestId("find-count").textContent;

afterEach(() => {
  document.documentElement.classList.remove("dark");
  document.documentElement.removeAttribute("data-color-scheme");
});

describe("FindBar", () => {
  it("says it is ready only once it is listening", () => {
    const bar = setup();
    expect(bar.subscribed()).toBe(true);
    expect(bar.api.ready).toHaveBeenCalledOnce();
    expect(bar.api.ready.mock.invocationCallOrder[0]).toBeGreaterThan(
      bar.api.onResult.mock.invocationCallOrder[0],
    );
  });

  it("takes the keyboard and the app's look when opened", () => {
    const bar = setup();
    bar.open({ dark: true, colorScheme: "forest" });

    expect(document.activeElement).toBe(bar.input);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(document.documentElement.getAttribute("data-color-scheme")).toBe("forest");
  });

  it("searches as the user types, and shows where the search stands", () => {
    const bar = setup();
    bar.open();

    fireEvent.change(bar.input, { target: { value: "al" } });
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    expect(count()).toBe("");
    bar.report(3, 1);

    expect(bar.api.find.mock.calls).toEqual([[newSearch("al")], [newSearch("alpha")]]);
    expect(count()).toBe("1/3");
  });

  it("steps with Enter and Shift+Enter, and with Cmd+G and Cmd+Shift+G", () => {
    const bar = setup();
    bar.open();
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.api.find.mockClear();

    fireEvent.keyDown(bar.input, { key: "Enter" });
    fireEvent.keyDown(bar.input, { key: "Enter", shiftKey: true });
    fireEvent.keyDown(bar.input, { key: "g", metaKey: true });
    fireEvent.keyDown(bar.input, { key: "G", metaKey: true, shiftKey: true });

    expect(bar.api.find.mock.calls).toEqual([
      [stepTo("alpha", true)],
      [stepTo("alpha", false)],
      [stepTo("alpha", true)],
      [stepTo("alpha", false)],
    ]);
  });

  it("uses Ctrl, not Cmd, off a Mac", () => {
    const bar = setup({ isMac: false });
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.api.find.mockClear();

    fireEvent.keyDown(bar.input, { key: "g", metaKey: true });
    fireEvent.keyDown(bar.input, { key: "g", ctrlKey: true });

    expect(bar.api.find.mock.calls).toEqual([[stepTo("alpha", true)]]);
  });

  it("steps with the buttons, keeping the keyboard in the query", () => {
    const bar = setup();
    bar.open();
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.report(3, 1);
    bar.api.find.mockClear();

    const next = screen.getByRole("button", { name: "Next match" });
    const previous = screen.getByRole("button", { name: "Previous match" });
    // Not focusing the button is what keeps typing going to the query.
    expect(fireEvent.mouseDown(next)).toBe(false);
    fireEvent.click(next);
    fireEvent.click(previous);

    expect(bar.api.find.mock.calls).toEqual([[stepTo("alpha", true)], [stepTo("alpha", false)]]);
  });

  it("does not step with nothing to step through", () => {
    const bar = setup();
    fireEvent.keyDown(bar.input, { key: "Enter" });
    expect(bar.api.find).not.toHaveBeenCalled();

    fireEvent.change(bar.input, { target: { value: "zzz" } });
    bar.report(0, 0);
    expect(screen.getByRole("button", { name: "Next match" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Previous match" })).toBeDisabled();
    expect(count()).toBe("0/0");
    expect(bar.input).toHaveAttribute("aria-invalid", "true");
  });

  it("closes on Escape and on the close button", () => {
    const bar = setup();
    fireEvent.keyDown(bar.input, { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(bar.api.close).toHaveBeenCalledTimes(2);
  });

  it("selects the query again on Cmd+F", () => {
    const bar = setup();
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.input.setSelectionRange(5, 5);

    const event = fireEvent.keyDown(bar.input, { key: "f", metaKey: true });

    expect(event).toBe(false);
    expect([bar.input.selectionStart, bar.input.selectionEnd]).toEqual([0, 5]);
  });

  it("emptying the query ends the search and its count", () => {
    const bar = setup();
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.report(3, 2);

    fireEvent.change(bar.input, { target: { value: "" } });

    expect(bar.api.find).toHaveBeenLastCalledWith(newSearch(""));
    expect(count()).toBe("");
  });

  it("reopened on a query, selects it and searches it again", () => {
    // Closing cleared the highlights.
    const bar = setup();
    fireEvent.change(bar.input, { target: { value: "alpha" } });
    bar.api.find.mockClear();

    bar.open();

    expect(bar.api.find.mock.calls).toEqual([[newSearch("alpha")]]);
    expect([bar.input.selectionStart, bar.input.selectionEnd]).toEqual([0, 5]);
  });

  it("waits for an IME composition to finish before searching", () => {
    const bar = setup();
    fireEvent.compositionStart(bar.input);
    fireEvent.change(bar.input, { target: { value: "にほ" } });
    expect(bar.api.find).not.toHaveBeenCalled();

    fireEvent.compositionEnd(bar.input, { target: { value: "日本" } });
    expect(bar.api.find).toHaveBeenLastCalledWith(newSearch("日本"));
  });

  it("stops listening when unmounted", () => {
    const bar = setup();
    bar.view.unmount();
    expect(bar.subscribed()).toBe(false);
  });
});

describe("theme", () => {
  it("round-trips the app's look onto the bar page", () => {
    const app = document.createElement("html");
    app.classList.add("dark");
    app.setAttribute("data-color-scheme", "slate");
    const page = document.createElement("html");
    page.setAttribute("data-color-scheme", "forest");

    applyTheme(themeSnapshot(app), page);
    expect(themeSnapshot(page)).toEqual({ dark: true, colorScheme: "slate" });

    applyTheme({ dark: false, colorScheme: null }, page);
    expect(page.classList.contains("dark")).toBe(false);
    expect(page.hasAttribute("data-color-scheme")).toBe(false);
  });
});
