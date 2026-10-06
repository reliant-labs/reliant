import { render, screen, fireEvent } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType } from "../../../gen/reliant/v1/approval_pb";
import type { ToolApprovalRequest } from "../../../api/client";

const approveMutate = vi.fn();
const denyMutate = vi.fn();
let pending = false;

vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: approveMutate, isPending: pending }),
  useDenyToolRequest: () => ({ mutate: denyMutate, isPending: false }),
}));

import { ActionApprovalCard } from "../ActionApprovalCard";

const slackApproval: ToolApprovalRequest = {
  id: "appr-1",
  chat_id: "chat-1",
  content_block_id: "wf:1",
  tool_call_id: "toolu_1",
  tool_name: "slack__message_post",
  title: "Post message in #general?",
  approval_type: ApprovalType.TOOL,
  status: ApprovalStatus.PENDING,
  created_at: new Date().toISOString(),
  params: { channel: "#general", text: "Ship it", blocks: [{ type: "section" }] },
  integration_name: "Slack",
  integration_icon: "slack",
};

function renderCard(approval: ToolApprovalRequest = slackApproval, surface: "desktop" | "mobile" = "desktop") {
  return render(
    <SurfaceProvider surface={surface}>
      <ActionApprovalCard approval={approval} chatId="chat-1" shortcutKey="⌘" />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  approveMutate.mockReset();
  denyMutate.mockReset();
  pending = false;
});

describe("ActionApprovalCard", () => {
  it("shows the action's logo, the human title and every parameter", () => {
    const { container } = renderCard();
    const card = screen.getByRole("region", { name: "Post message in #general?" });
    expect(card).toBeInTheDocument();
    expect(container.querySelector('[data-integration-logo="slack"]')).not.toBeNull();
    expect(screen.getByText(/Slack · Needs your approval/)).toBeInTheDocument();
    expect(screen.getByText("channel")).toBeInTheDocument();
    expect(screen.getByText("#general")).toBeInTheDocument();
    expect(screen.getByText("Ship it")).toBeInTheDocument();
    // Structured values are shown in full, as JSON.
    expect(screen.getByText(/"type": "section"/)).toBeInTheDocument();
  });

  it("answers Allow once, Always allow and Deny", () => {
    renderCard();
    fireEvent.click(screen.getByRole("button", { name: /allow once/i }));
    expect(approveMutate).toHaveBeenLastCalledWith({ chatId: "chat-1", requestId: "appr-1", actionTaken: "allow_once" });

    fireEvent.click(screen.getByRole("button", { name: /always allow/i }));
    expect(approveMutate).toHaveBeenLastCalledWith({ chatId: "chat-1", requestId: "appr-1", actionTaken: "always_allow" });

    fireEvent.click(screen.getByRole("button", { name: /^deny$/i }));
    expect(denyMutate).toHaveBeenCalledWith({ chatId: "chat-1", requestId: "appr-1", actionTaken: "deny" });
  });

  it("explains that Always allow stops asking and where to revoke it", () => {
    renderCard();
    const always = screen.getByRole("button", { name: /always allow/i });
    expect(always).toHaveAccessibleDescription(/stops asking about it\. revoke in settings/i);
  });

  it("takes focus when nothing has it, on the card rather than a button", () => {
    renderCard();
    expect(document.activeElement).toBe(screen.getByTestId("action-approval-card"));
  });

  it("never pulls focus out of the composer", () => {
    const composer = document.createElement("textarea");
    document.body.appendChild(composer);
    composer.focus();
    renderCard();
    expect(document.activeElement).toBe(composer);
    composer.remove();
  });

  it("disables its buttons while an answer is in flight", () => {
    pending = true;
    renderCard();
    for (const name of [/allow once/i, /always allow/i, /^deny$/i]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
    }
  });

  it("gives the three answers touch targets on mobile", () => {
    renderCard(slackApproval, "mobile");
    for (const name of [/allow once/i, /always allow/i, /^deny$/i]) {
      expect(screen.getByRole("button", { name }).className).toMatch(/min-h-\[44px\]/);
    }
  });
});
