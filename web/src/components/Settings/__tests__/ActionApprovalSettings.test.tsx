import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listSettings: vi.fn(),
  deleteSetting: vi.fn(),
}));

vi.mock("../../../api/client", () => ({
  api: { settings: { listSettings: mocks.listSettings, deleteSetting: mocks.deleteSetting } },
}));

import { ActionApprovalSettings, allowedActions } from "../ActionApprovalSettings";

const slackRow = {
  key: "tool.approval.slack__message_post",
  value: JSON.stringify({ decision: "always_allow", display_name: "Post message", integration: "Slack", icon: "slack" }),
};

beforeEach(() => {
  mocks.listSettings.mockReset();
  mocks.deleteSetting.mockReset();
});

describe("allowedActions", () => {
  it("keeps only the always-allow rows, labelled from their value", () => {
    const rows = allowedActions([
      slackRow,
      { key: "tool.bindings.generate_image", value: "{}" },
      { key: "tool.approval.http__request", value: "not json" },
    ]);
    expect(rows).toEqual([
      { key: "tool.approval.http__request", tool: "http__request", displayName: "http__request", integration: undefined, icon: undefined },
      { key: slackRow.key, tool: "slack__message_post", displayName: "Post message", integration: "Slack", icon: "slack" },
    ]);
  });
});

describe("ActionApprovalSettings", () => {
  it("lists what is always allowed and revokes it, so the action asks again", async () => {
    mocks.listSettings.mockResolvedValue({ settings: [slackRow], total: 1 });
    mocks.deleteSetting.mockResolvedValue({ success: true });
    render(<ActionApprovalSettings />);

    expect(await screen.findByText("Post message")).toBeInTheDocument();
    expect(screen.getByText(/Slack ·/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Revoke always allow for Post message (Slack)" }));
    expect(mocks.deleteSetting).toHaveBeenCalledWith("tool.approval.slack__message_post");
    await waitFor(() => expect(screen.queryByText("Post message")).not.toBeInTheDocument());
    expect(screen.getByText(/Nothing is always allowed/)).toBeInTheDocument();
  });

  it("keeps the row and says so when revoking fails", async () => {
    mocks.listSettings.mockResolvedValue({ settings: [slackRow], total: 1 });
    mocks.deleteSetting.mockRejectedValue(new Error("offline"));
    render(<ActionApprovalSettings />);

    await userEvent.click(await screen.findByRole("button", { name: /revoke always allow for post message/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't revoke Post message");
    expect(screen.getByText("Post message")).toBeInTheDocument();
  });

  it("says when nothing is always allowed", async () => {
    mocks.listSettings.mockResolvedValue({ settings: [], total: 0 });
    render(<ActionApprovalSettings />);
    expect(await screen.findByText(/Nothing is always allowed/)).toBeInTheDocument();
  });
});
