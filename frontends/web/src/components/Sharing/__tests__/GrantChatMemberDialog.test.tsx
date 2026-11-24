/**
 * The grant dialog (chat-collaboration.md §2.1, §3.2): View by default; a
 * picker over the org's live members minus the owner and current members;
 * the reply disclosure, in plain words and honest about what works today,
 * before Reply or Manage is saved; the cap; refusals shown in place.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";

const mocks = vi.hoisted(() => ({
  grant: vi.fn(async () => ({})),
  toastSuccess: vi.fn(),
  roster: [] as Array<Record<string, unknown>>,
}));

vi.mock("@/hooks/chat-member-queries", () => ({
  useOrgRoster: () => ({ members: mocks.roster, isLoading: false, error: null }),
  useGrantChatMember: () => ({ grant: mocks.grant, isPending: false }),
}));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { GrantChatMemberDialog } = await import("../GrantChatMemberDialog");

const member = (externalId: string, name: string, email: string) => ({ userId: `cp-${externalId}`, externalId, name, email });

beforeEach(() => {
  vi.clearAllMocks();
  mocks.roster = [
    member("owner", "Olive Owner", "olive@acme.dev"),
    member("dana", "Dana", "dana@acme.dev"),
    member("eli", "", "eli@acme.dev"),
    member("max", "Max Member", "max@acme.dev"),
    // No sign-in subject: nothing this app could grant to.
    { userId: "cp-ghost", externalId: "", name: "Ghost", email: "ghost@acme.dev" },
  ];
});

function renderDialog(props: Partial<Parameters<typeof GrantChatMemberDialog>[0]> = {}) {
  const onClose = vi.fn();
  render(
    <GrantChatMemberDialog
      chatId="c1"
      orgId="acme"
      open
      onClose={onClose}
      ownerUserId="owner"
      ownerName="Olive Owner"
      callerIsOwner
      memberUserIds={["max"]}
      memberCount={1}
      {...props}
    />,
  );
  return { onClose };
}

function pick(label: string) {
  fireEvent.click(screen.getByRole("button", { name: "Person" }));
  fireEvent.click(screen.getByRole("option", { name: new RegExp(label) }));
}

describe("GrantChatMemberDialog", () => {
  it("offers live members only, never the owner or a current member", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Person" }));
    const names = screen.getAllByRole("option").map((o) => o.textContent);
    expect(names.some((n) => n?.includes("Dana"))).toBe(true);
    expect(names.some((n) => n?.includes("eli@acme.dev"))).toBe(true);
    for (const absent of ["Olive Owner", "Max Member", "Ghost"]) {
      expect(names.some((n) => n?.includes(absent))).toBe(false);
    }
  });

  it("defaults to View, shows no disclosure for it, and grants View", async () => {
    const { onClose } = renderDialog();
    expect(screen.getByTestId("grant-member-role-view")).toBeChecked();
    expect(screen.queryByTestId("grant-member-disclosure")).not.toBeInTheDocument();
    expect(screen.getByTestId("grant-member-confirm")).toBeDisabled(); // no one picked yet
    pick("Dana");
    expect(screen.getByTestId("grant-member-confirm")).toHaveTextContent("Add with View");
    fireEvent.click(screen.getByTestId("grant-member-confirm"));
    await waitFor(() => expect(mocks.grant).toHaveBeenCalledWith("c1", "dana", ChatMemberRole.VIEW));
    expect(onClose).toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Added Dana with View");
  });

  it("names the consequence of Reply before saving, and what works today", async () => {
    renderDialog();
    pick("Dana");
    fireEvent.click(screen.getByTestId("grant-member-role-reply"));
    const disclosure = screen.getByTestId("grant-member-disclosure");
    expect(disclosure).toHaveTextContent("Once it ships, their messages go to your agent");
    expect(disclosure).toHaveTextContent("your machine, with your credentials, and the organization pays for it");
    expect(disclosure).toHaveTextContent("read any file you can read");
    expect(disclosure).toHaveTextContent("approve the tool calls");
    expect(disclosure).toHaveTextContent("Tool results stay hidden from them");
    // Honest about the reply path not having shipped.
    expect(within(disclosure).getByTestId("grant-member-today")).toHaveTextContent(
      "Replying is not available yet. Until it is, they can read this chat and nothing more.",
    );
    fireEvent.click(screen.getByTestId("grant-member-confirm"));
    await waitFor(() => expect(mocks.grant).toHaveBeenCalledWith("c1", "dana", ChatMemberRole.REPLY));
  });

  it("says Manage can change people today, and names the owner when a manager grants", () => {
    renderDialog({ callerIsOwner: false });
    fireEvent.click(screen.getByTestId("grant-member-role-manage"));
    const disclosure = screen.getByTestId("grant-member-disclosure");
    expect(disclosure).toHaveTextContent("their messages go to Olive Owner's agent, not to Olive Owner");
    expect(disclosure).toHaveTextContent("add, change or remove people");
  });

  it("changes a member's level, starting from the one they hold", async () => {
    renderDialog({ target: { userId: "max", name: "Max Member", role: ChatMemberRole.VIEW } });
    expect(screen.queryByRole("button", { name: "Person" })).not.toBeInTheDocument();
    expect(screen.getByText("Change Max Member's access")).toBeInTheDocument();
    expect(screen.getByTestId("grant-member-confirm")).toBeDisabled(); // unchanged
    fireEvent.click(screen.getByTestId("grant-member-role-manage"));
    fireEvent.click(screen.getByTestId("grant-member-confirm"));
    await waitFor(() => expect(mocks.grant).toHaveBeenCalledWith("c1", "max", ChatMemberRole.MANAGE));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Max Member now has Manage");
  });

  it("refuses to add a 26th member", () => {
    renderDialog({ memberCount: 25 });
    expect(screen.getByTestId("grant-member-full")).toHaveTextContent("25 people");
    expect(screen.getByRole("button", { name: "Person" })).toBeDisabled();
    expect(screen.getByTestId("grant-member-confirm")).toBeDisabled();
  });

  it("shows a refusal in place and stays open", async () => {
    const err = new ConnectError("nope", Code.FailedPrecondition);
    err.metadata.set("x-forge-error-reason", "not_org_member");
    mocks.grant.mockRejectedValueOnce(err);
    const { onClose } = renderDialog();
    pick("Dana");
    fireEvent.click(screen.getByTestId("grant-member-confirm"));
    expect(await screen.findByRole("alert")).toHaveTextContent("not a member of this chat's organization");
    expect(onClose).not.toHaveBeenCalled();
  });
});
