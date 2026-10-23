/**
 * The chat settings popover on a phone. It was a fixed 360px box anchored to a
 * button partway across a 390px composer, so it ran off the right edge. Below
 * `md` it is a bottom sheet over a scrim (the classes are what jsdom can see of
 * that; the layout itself is pinned by e2e/mobile.spec.ts), it is a named
 * dialog, the scrim closes it, and its header controls have names.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChatSettingsPopover } from "../ChatSettingsPopover";
import type { InputDef } from "../../../../lib/inputHelpers";

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("../../../../store/globalDataStore", () => ({
  useModels: () => ({ models: [], loading: false, error: null }),
}));

const modelInput = {
  type: "model",
  config: {
    case: "modelInput",
    value: { base: { description: "LLM model to use", ui: "config" }, default: { id: "", tags: [], providers: [] } },
  },
} as unknown as InputDef;

function renderPopover(onClose = vi.fn()) {
  render(
    <div className="relative">
      <ChatSettingsPopover isOpen onClose={onClose} inputs={{ model: modelInput }} values={{}} onChange={() => {}} />
    </div>,
  );
  return onClose;
}

describe("ChatSettingsPopover on a phone", () => {
  it("is a named dialog that becomes a full-width bottom sheet below md", () => {
    renderPopover();
    const dialog = screen.getByRole("dialog", { name: "Chat settings" });
    // Desktop: the 360px popover. Below md: pinned to the bottom, full width,
    // no fixed width, scrolling inside itself, clear of the home indicator.
    expect(dialog).toHaveClass("w-[360px]");
    for (const cls of [
      "max-md:fixed",
      "max-md:inset-x-0",
      "max-md:bottom-0",
      "max-md:w-auto",
      "max-md:max-h-[85dvh]",
      "max-md:overflow-y-auto",
      "max-md:pb-[env(safe-area-inset-bottom)]",
    ]) {
      expect(dialog).toHaveClass(cls);
    }
  });

  it("closes from the scrim, which only a phone shows", async () => {
    const onClose = renderPopover();
    const scrim = document.querySelector('[aria-hidden="true"].max-md\\:block') as HTMLElement;
    expect(scrim).not.toBeNull();
    expect(scrim).toHaveClass("hidden");
    await userEvent.click(scrim);
    expect(onClose).toHaveBeenCalled();
  });

  it("names its close control, and sizes it for a finger", () => {
    renderPopover();
    const close = screen.getByRole("button", { name: "Close settings" });
    expect(close).toHaveClass("touch-target");
  });
});
