import { QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { useAuthStore } from "@/store/authStore";
import { useLongPastePreferences } from "@/hooks/useLongPastePreferences";
import { DEFAULT_LONG_PASTE_PREFERENCES, useUpdateLongPastePreferences } from "@/hooks/settings-queries";
import { settingsServiceKeys } from "@/hooks/settings-service-hooks_gen";
import { LongPasteSettings } from "../LongPasteSettings";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
let stored: Record<string, string>;
let writes: number;
let refuse: boolean;
beforeEach(() => {
  stored = {};
  writes = 0;
  refuse = false;
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(mockTransport({
    "SettingsService.GetPreferences": () => ({ additional: { ...stored } }),
    "SettingsService.UpdatePreferences": (req) => {
      writes++;
      if (refuse) throw new Error("refused");
      stored = { ...stored, ...(req as { additional: Record<string, string> }).additional };
      return { success: true };
    },
  }));
});
afterEach(() => { cleanup(); queryClient.clear(); setTransport(null); useAuthStore.setState({ session: null }); });
const loaded = () => waitFor(() => expect(queryClient.getQueryData(settingsServiceKeys.getPreferences({}))).toBeDefined());

describe("long-paste preferences", () => {
  it("defaults without writing on mount", async () => {
    const hook = renderHook(useLongPastePreferences, { wrapper });
    expect(hook.result.current).toEqual(DEFAULT_LONG_PASTE_PREFERENCES);
    await loaded();
    expect(writes).toBe(0);
  });
  it("reads saved values and rejects malformed thresholds independently", async () => {
    stored = { "longPaste.enabled": "false", "longPaste.characterThreshold": "9000", "longPaste.lineThreshold": "-1" };
    const hook = renderHook(useLongPastePreferences, { wrapper });
    await waitFor(() => expect(hook.result.current).toEqual({ enabled: false, characterThreshold: 9000, lineThreshold: 100 }));
  });
  it.each(["0", "NaN", "Infinity", "1.5", "9007199254740992", ""]) ("falls back for invalid stored threshold %s", async (value) => {
    stored = { "longPaste.characterThreshold": value, "longPaste.lineThreshold": value };
    const hook = renderHook(useLongPastePreferences, { wrapper });
    await loaded();
    expect(hook.result.current).toEqual(DEFAULT_LONG_PASTE_PREFERENCES);
  });
  it("persists controls, updates the composer reader and disables thresholds when off", async () => {
    const hook = renderHook(useLongPastePreferences, { wrapper });
    render(<LongPasteSettings />, { wrapper });
    const characters = screen.getByLabelText("Character threshold");
    await waitFor(() => expect(characters).not.toBeDisabled());
    fireEvent.change(characters, { target: { value: "6500" } });
    fireEvent.blur(characters);
    await waitFor(() => expect(hook.result.current.characterThreshold).toBe(6500));
    await waitFor(() => expect(stored["longPaste.characterThreshold"]).toBe("6500"));
    const toggle = screen.getByRole("switch", { name: "Automatically attach long pastes" });
    await waitFor(() => expect(toggle).not.toBeDisabled());
    fireEvent.click(toggle);
    await waitFor(() => expect(hook.result.current.enabled).toBe(false));
    expect(characters).toBeDisabled();
    expect(screen.getByLabelText("Line threshold")).toBeDisabled();
  });
  it("keeps invalid input editable without persisting it", async () => {
    render(<LongPasteSettings />, { wrapper });
    const lines = screen.getByLabelText("Line threshold");
    await waitFor(() => expect(lines).not.toBeDisabled());
    fireEvent.change(lines, { target: { value: "0" } });
    fireEvent.blur(lines);
    expect(screen.getByRole("alert")).toHaveTextContent("positive whole number");
    expect(writes).toBe(0);
    fireEvent.change(lines, { target: { value: "250" } });
    fireEvent.blur(lines);
    await waitFor(() => expect(stored["longPaste.lineThreshold"]).toBe("250"));
  });
  it("disables edits after a load failure and lets the user retry", async () => {
    let fail = true;
    setTransport(mockTransport({
      "SettingsService.GetPreferences": () => {
        if (fail) throw new ConnectError("refused", Code.PermissionDenied);
        return { additional: { "longPaste.characterThreshold": "8000" } };
      },
    }));
    render(<LongPasteSettings />, { wrapper });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Could not load"));
    expect(screen.getByLabelText("Character threshold")).toBeDisabled();
    expect(screen.getByRole("switch")).toBeDisabled();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByLabelText("Character threshold")).toHaveValue(8000));
    expect(screen.getByLabelText("Character threshold")).not.toBeDisabled();
    expect(writes).toBe(0);
  });
  it("rolls back failed saves and reports the error", async () => {
    refuse = true;
    const hook = renderHook(useLongPastePreferences, { wrapper });
    render(<LongPasteSettings />, { wrapper });
    const toggle = screen.getByRole("switch", { name: "Automatically attach long pastes" });
    await waitFor(() => expect(toggle).not.toBeDisabled());
    fireEvent.click(toggle);
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Could not save"));
    expect(hook.result.current.enabled).toBe(true);
  });
  it("refreshes mounted readers when another device changes preferences", async () => {
    const hook = renderHook(useLongPastePreferences, { wrapper });
    await loaded();
    stored = { "longPaste.lineThreshold": "400" };
    await act(() => queryClient.invalidateQueries({ queryKey: settingsServiceKeys.all }));
    await waitFor(() => expect(hook.result.current.lineThreshold).toBe(400));
  });
  it("does not send invalid threshold writes", async () => {
    const writer = renderHook(useUpdateLongPastePreferences, { wrapper });
    await expect(writer.result.current.mutateAsync({ lineThreshold: 0 })).rejects.toThrow("positive whole numbers");
    await expect(writer.result.current.mutateAsync({ characterThreshold: 1.5 })).rejects.toThrow("positive whole numbers");
    expect(writes).toBe(0);
  });
  it("resets all values and preserves unrelated preferences", async () => {
    stored = { "longPaste.enabled": "false", "longPaste.characterThreshold": "999", "longPaste.lineThreshold": "5", browserDefaultPage: "https://example.com" };
    const hook = renderHook(useLongPastePreferences, { wrapper });
    const writer = renderHook(useUpdateLongPastePreferences, { wrapper });
    await loaded();
    await act(() => writer.result.current.mutateAsync(DEFAULT_LONG_PASTE_PREFERENCES));
    await waitFor(() => expect(hook.result.current).toEqual(DEFAULT_LONG_PASTE_PREFERENCES));
    expect(stored.browserDefaultPage).toBe("https://example.com");
  });
});
