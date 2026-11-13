import { screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { InboxAutomationFailingSchema } from "@/gen/reliant/v1/inbox_pb";
import { InboxItem } from "../InboxItem";
import { failingItem, renderInboxAt } from "./inboxTestUtils";

describe("attention incident row", () => {
  it("shows semantic incident copy and opens its run without a meaningless trigger toggle", async () => {
    const item = failingItem({
      itemId: "automation_failing:incident:episode-1",
      chatId: "chat-last",
      triggerId: "",
      chatTitle: "PR 42 repair",
      payload: {
        case: "automationFailing",
        value: create(InboxAutomationFailingSchema, {
          incidentKey: "pr:42",
          attentionState: "action_required",
          failureClass: "recovery_expired",
          detail: "Recovery owner did not finish",
          lastRunChatId: "chat-last",
        }),
      },
    });
    renderInboxAt(<ul><InboxItem item={item} showProject onDismiss={vi.fn()} /></ul>);
    expect(await screen.findByText("Recovery timed out · Recovery owner did not finish")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open run" })).toHaveAttribute("href", "/workflows/runs/chat-last");
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dismiss PR 42 repair" })).toBeInTheDocument();
  });
});
