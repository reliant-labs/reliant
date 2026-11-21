import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { EXAMPLE_PROMPTS, examplePromptFor } from "../examplePrompt";

const active = vi.hoisted(() => ({ chatId: "chat-1" as string | null }));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => active.chatId,
}));

vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

vi.mock("../../../lib/fileOpener", () => ({ useFileOpener: () => vi.fn() }));

import { ChatTextArea } from "../ChatTextArea";

afterEach(() => {
  vi.restoreAllMocks();
  active.chatId = "chat-1";
});

/**
 * The composer's example prompt is a function of the chat, never of
 * Math.random: with Math.random, how many draws other code made before the
 * composer mounted decided the example, so a route change moved the visual
 * baseline of every chat surface.
 */
describe("examplePromptFor", () => {
  it("is the same for the same chat, every time", () => {
    expect(examplePromptFor("chat-1")).toBe(examplePromptFor("chat-1"));
    expect(EXAMPLE_PROMPTS).toContain(examplePromptFor("chat-1"));
  });

  it("never draws from Math.random", () => {
    const random = vi.spyOn(Math, "random");
    examplePromptFor("chat-1");
    examplePromptFor(null);
    expect(random).not.toHaveBeenCalled();
  });

  it("varies across chats", () => {
    const picks = new Set(Array.from({ length: 200 }, (_, i) => examplePromptFor(`chat-${i}`)));
    expect(picks.size).toBeGreaterThan(EXAMPLE_PROMPTS.length / 2);
  });

  it("keys a new chat by the UTC day: stable within it, rotating across days", () => {
    const morning = examplePromptFor(null, new Date("2026-11-18T00:05:00Z"));
    expect(examplePromptFor(undefined, new Date("2026-11-18T23:55:00Z"))).toBe(morning);
    const days = new Set(Array.from({ length: 30 }, (_, i) => examplePromptFor(null, new Date(Date.UTC(2026, 10, 1 + i)))));
    expect(days.size).toBeGreaterThan(1);
  });
});

describe("ChatTextArea placeholder", () => {
  const placeholder = () => screen.getByTestId("chat-input").getAttribute("placeholder");

  it("does not depend on Math.random's draws", () => {
    vi.spyOn(Math, "random").mockReturnValue(0);
    const first = render(<ChatTextArea value="" onChange={vi.fn()} onSend={vi.fn()} />);
    const atZero = placeholder();
    first.unmount();

    vi.spyOn(Math, "random").mockReturnValue(0.999);
    render(<ChatTextArea value="" onChange={vi.fn()} onSend={vi.fn()} />);
    expect(placeholder()).toBe(atZero);
    expect(atZero).toBe(`Ask Reliant, e.g. "${examplePromptFor("chat-1")}"`);
  });

  it("follows the active chat", () => {
    const view = render(<ChatTextArea value="" onChange={vi.fn()} onSend={vi.fn()} />);
    const other = Array.from({ length: 50 }, (_, i) => `chat-other-${i}`).find(
      (id) => examplePromptFor(id) !== examplePromptFor("chat-1"),
    )!;
    active.chatId = other;
    view.rerender(<ChatTextArea value="" onChange={vi.fn()} onSend={vi.fn()} />);
    expect(placeholder()).toBe(`Ask Reliant, e.g. "${examplePromptFor(other)}"`);
  });
});
