import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SystemNotificationMessage } from "../SystemNotificationMessage";
import type { Message } from "../../../api/client";
import { ContentBlockType, DisplayStyle, MessageRole } from "../../../gen/reliant/v1/chat_pb";

function notice(text: string, displayStyle: DisplayStyle = DisplayStyle.WARNING): Message {
  return {
    id: "m",
    chatId: "c",
    thread: "c",
    role: MessageRole.SYSTEM,
    displayStyle,
    contentBlocks: [{ id: "b", index: 0, type: ContentBlockType.TEXT, content: text }],
    createdAt: "2024-01-01T00:00:00.000Z",
  } as unknown as Message;
}

describe("SystemNotificationMessage — account and provider switches", () => {
  it("bolds the account names of an account switch", () => {
    const { container } = render(
      <SystemNotificationMessage
        message={notice("Claude account **alice@acme.com** reached its 5-hour limit (resets <time:2026-10-19T15:40:00Z>). Continuing on **bob@acme.com**.")}
      />,
    );
    const strong = Array.from(container.querySelectorAll("strong")).map((e) => e.textContent);
    expect(strong).toEqual(["alice@acme.com", "bob@acme.com"]);
    expect(container.firstElementChild).toHaveAttribute("data-notice-kind", "account_switch");
    expect(container.textContent).not.toContain("**");
    expect(container.textContent).not.toContain("<time:");
    const local = new Date("2026-10-19T15:40:00Z").toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
    expect(container.textContent).toContain(`resets ${local}`);
  });

  it("bolds the model of a cross-provider switch and of a return", () => {
    const { container, rerender } = render(
      <SystemNotificationMessage
        message={notice("Claude is rate-limited on all your accounts until <time:2026-10-19T15:40:00Z>. Continuing on **GPT-5.6 Sol · Codex** as this chat allows; returning to Claude after the reset.")}
      />,
    );
    expect(container.querySelector("strong")).toHaveTextContent("GPT-5.6 Sol · Codex");
    expect(container.firstElementChild).toHaveAttribute("data-notice-kind", "provider_switch");

    rerender(<SystemNotificationMessage message={notice("Claude's limit has reset. Back on **Claude 5.5 Opus · Claude**.")} />);
    expect(container.querySelector("strong")).toHaveTextContent("Claude 5.5 Opus · Claude");
    expect(container.firstElementChild).toHaveAttribute("data-notice-kind", "return");
  });

  it("leaves other warnings as plain text", () => {
    const { container } = render(<SystemNotificationMessage message={notice("Max turns reached.")} />);
    expect(screen.getByText("Max turns reached.")).toBeInTheDocument();
    expect(container.querySelector("strong")).toBeNull();
    expect(container.firstElementChild).not.toHaveAttribute("data-notice-kind");
  });
});
