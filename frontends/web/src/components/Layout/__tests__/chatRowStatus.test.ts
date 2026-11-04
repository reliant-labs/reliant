import { describe, expect, it } from "vitest";
import { ChatActivity } from "../../../gen/reliant/v1/chat_pb";
import { chatListIndicators } from "../../../lib/runStatus";
import { CHAT_ROW_CHIP_CLASS, chatRowChip } from "../chatRowStatus";

const chip = (activity: ChatActivity, workingThreads = 0) =>
  chatRowChip(chatListIndicators({ activity, workingThreads, unread: false }));

describe("chat row status chip", () => {
  it("says the state in words", () => {
    expect(chip(ChatActivity.RUNNING)).toMatchObject({ label: "Running", tone: "running" });
    expect(chip(ChatActivity.AWAITING_INPUT)).toMatchObject({ label: "Needs you", tone: "attention" });
    expect(chip(ChatActivity.ERROR)).toMatchObject({ label: "Failed", tone: "failed" });
    expect(chip(ChatActivity.PAUSED)).toMatchObject({ label: "Paused", tone: "neutral" });
    expect(chip(ChatActivity.IDLE)).toBeNull();
  });

  it("keeps the fuller phrase for a question asked while agents still run", () => {
    expect(chip(ChatActivity.AWAITING_INPUT, 2)?.detail).toBe("Needs your input · 2 agents still running");
  });

  it("never paints a status green: green is for success", () => {
    for (const cls of Object.values(CHAT_ROW_CHIP_CLASS)) expect(cls).not.toMatch(/success/);
  });
});
