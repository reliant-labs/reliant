/**
 * The invite dialog: an address and a role go to InviteMember in the org it
 * was opened for; on success it says whether mail went out and offers the
 * link either way (mail can be off, or land in spam); a full org and a refused
 * invite say why in words; only an owner can invite an owner.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { setTransport } from "@/lib/connect";
import { mockTransport, type MockHandler } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { InviteDialog } from "../InviteDialog";

const org = (role: string): MyOrg => ({ id: "org-a", name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true });
const LINK = "https://app.test/accept-invite#rinv_9f3KQeAbCdEfGhIjKlMnOpQrStUv";

const SENT = { emailSent: true, acceptUrl: LINK, invitation: { email: "bob@example.com" } };
const NO_SEATS_USED = { members: 1, pendingInvitations: 0, limit: 3 };


function open(role = "owner", seats = NO_SEATS_USED, handler: MockHandler = vi.fn((_req: unknown) => SENT)) {
  setTransport(mockTransport({ "OrganizationService.InviteMember": handler }));
  renderWithQuery(<InviteDialog open onClose={vi.fn()} org={org(role)} seats={seats} />);
  return { seen: handler as ReturnType<typeof vi.fn> };
}

beforeEach(() => setTransport(null));

describe("InviteDialog", () => {
  it("sends the address and role to the org it was opened for, then says it was sent", async () => {
    const { seen } = open();
    await userEvent.type(screen.getByTestId("invite-email"), "bob@example.com");
    await userEvent.selectOptions(screen.getByTestId("invite-role"), "admin");
    await userEvent.click(screen.getByTestId("invite-submit"));
    await waitFor(() => expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a", email: "bob@example.com", role: "admin" })));
    expect(await screen.findByTestId("invite-result-status")).toHaveTextContent("Sent to bob@example.com");
    // The link is offered even when mail went out.
    expect(screen.getByTestId("invite-link")).toHaveTextContent(LINK);
  });

  it("when mail is off, the link is the delivery and copies to the clipboard", async () => {
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    open("owner", NO_SEATS_USED, { response: { ...SENT, emailSent: false } });
    await userEvent.type(screen.getByTestId("invite-email"), "bob@example.com");
    await userEvent.click(screen.getByTestId("invite-submit"));
    expect(await screen.findByTestId("invite-result-status")).toHaveTextContent(/couldn't email bob@example.com/);
    await userEvent.click(screen.getByTestId("invite-copy-link"));
    expect(writeText).toHaveBeenCalledWith(LINK);
    expect(screen.getByTestId("invite-copy-link")).toHaveTextContent("Copied");
  });

  it("does not send an address that is not one", async () => {
    const { seen } = open();
    await userEvent.type(screen.getByTestId("invite-email"), "bob");
    expect(screen.getByTestId("invite-submit")).toBeDisabled();
    expect(seen).not.toHaveBeenCalled();
  });

  it("says when every seat is taken, and shows the refusal's words", async () => {
    open("owner", { members: 2, pendingInvitations: 1, limit: 3 }, {
      error: new ConnectError("prose", Code.ResourceExhausted, { "x-forge-error-reason": "seat_limit_reached" }),
    });
    expect(screen.getByTestId("members-seats")).toHaveTextContent("3 of 3 seats (1 pending)");
    expect(screen.getByText(/free a seat first/)).toBeInTheDocument();
    await userEvent.type(screen.getByTestId("invite-email"), "bob@example.com");
    await userEvent.click(screen.getByTestId("invite-submit"));
    expect(await screen.findByTestId("invite-error")).toHaveTextContent(/Every seat in this organization is taken/);
  });

  it("an admin cannot invite an owner; an owner can", async () => {
    open("admin");
    expect(within(screen.getByTestId("invite-role")).queryByRole("option", { name: "Owner" })).toBeNull();
  });

  it("an owner can invite an owner", async () => {
    open("owner");
    expect(within(screen.getByTestId("invite-role")).getByRole("option", { name: "Owner" })).toBeInTheDocument();
  });

  it("a re-invite of a pending address says it was sent again", async () => {
    open("owner", NO_SEATS_USED, { response: { ...SENT, resent: true } });
    await userEvent.type(screen.getByTestId("invite-email"), "bob@example.com");
    await userEvent.click(screen.getByTestId("invite-submit"));
    expect(await screen.findByTestId("invite-result-status")).toHaveTextContent("Sent again to bob@example.com");
  });
});
