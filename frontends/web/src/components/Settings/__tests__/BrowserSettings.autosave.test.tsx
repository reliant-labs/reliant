/** The homepage saves itself when the field is left, like every other setting. */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ update: vi.fn(async () => {}), success: vi.fn() }));
vi.mock("../../../hooks/settings-queries", () => ({
  FALLBACK_BROWSER_PAGE: "https://www.google.com",
  useBrowserPreferences: () => ({ data: { defaultPage: "https://example.com", openLinksInApp: true }, isPending: false }),
  useUpdateBrowserPreferences: () => ({ mutateAsync: mocks.update }),
}));
vi.mock("../../../lib/toast-manager", () => ({ toast: { success: mocks.success, error: vi.fn() } }));

const { BrowserSettings } = await import("../BrowserSettings");

describe("Built-in browser homepage", () => {
  it("is a labelled field that saves on blur, with no Save button", async () => {
    render(<BrowserSettings />);
    expect(screen.queryByRole("button", { name: /save/i })).not.toBeInTheDocument();
    const field = screen.getByLabelText("Default homepage");
    fireEvent.change(field, { target: { value: "github.com" } });
    await act(async () => {
      fireEvent.blur(field);
    });
    expect(mocks.update).toHaveBeenCalledWith({ defaultPage: "https://github.com" });
    expect(mocks.success).toHaveBeenCalledWith("Homepage saved");
  });

  it("does not save an unchanged or emptied field", async () => {
    mocks.update.mockClear();
    render(<BrowserSettings />);
    const field = screen.getByLabelText("Default homepage");
    fireEvent.change(field, { target: { value: "  " } });
    await act(async () => {
      fireEvent.keyDown(field, { key: "Enter" });
    });
    expect(mocks.update).not.toHaveBeenCalled();
    expect(field).toHaveValue("https://example.com");
  });
});
