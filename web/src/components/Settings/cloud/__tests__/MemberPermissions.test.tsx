import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

// MEMBER PERMISSIONS. Who in the org may deploy, manage secrets, manage
// domains — and who may change that.
//
// The clip is the property worth pinning at this layer: a member may grant
// only what they themselves hold. The server enforces it regardless, so what
// these tests check is that the UI does not OFFER an edit it knows will be
// refused, which is the difference between a disabled checkbox and a confusing
// round trip.

const listMemberPermissions = vi.fn();
const updateMemberPermissions = vi.fn();

vi.mock("../../../../services/controlPlane/memberPermissions", () => ({
  listMemberPermissions: () => listMemberPermissions(),
  updateMemberPermissions: (userId: string, scopes: string[]) =>
    updateMemberPermissions(userId, scopes),
}));

import { MemberPermissions } from "../MemberPermissions";

const ADMIN_SET = [
  "deploy:read",
  "deploy:write",
  "secret:read",
  "secret:write",
  "domain:read",
  "domain:write",
  "cluster:manage",
  "token:read",
  "token:write",
];

function roster(grantable: string[] = ADMIN_SET) {
  return {
    members: [
      { userId: "u-admin", email: "admin@example.test", role: "admin", scopes: ADMIN_SET },
      { userId: "u-dev", email: "dev@example.test", role: "member", scopes: ["deploy:read"] },
    ],
    grantable,
  };
}

describe("MemberPermissions", () => {
  beforeEach(() => {
    listMemberPermissions.mockReset().mockResolvedValue(roster());
    updateMemberPermissions.mockReset().mockImplementation(
      (userId: string, scopes: string[]) =>
        Promise.resolve({ userId, email: "dev@example.test", role: "member", scopes })
    );
  });

  it("lists every member, including one holding almost nothing", async () => {
    render(<MemberPermissions />);
    expect(await screen.findByText("dev@example.test")).toBeInTheDocument();
    expect(screen.getByText("admin@example.test")).toBeInTheDocument();
  });

  it("grants a permission by sending the member's complete new set", async () => {
    // The request replaces the whole set, so granting deploy:write must send
    // the existing deploy:read alongside it — sending only the new one would
    // silently revoke everything else.
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");

    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i }));

    await waitFor(() => expect(updateMemberPermissions).toHaveBeenCalledTimes(1));
    const [userId, scopes] = updateMemberPermissions.mock.calls[0];
    expect(userId).toBe("u-dev");
    expect(scopes).toContain("deploy:write");
    expect(scopes).toContain("deploy:read");
  });

  it("revokes by omitting the permission from the set", async () => {
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");

    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test deploy:read/i }));

    await waitFor(() => expect(updateMemberPermissions).toHaveBeenCalledTimes(1));
    const [, scopes] = updateMemberPermissions.mock.calls[0];
    expect(scopes).not.toContain("deploy:read");
  });

  it("disables what the caller cannot grant", async () => {
    // A member holding token:write but not secret:write may administer
    // permissions and still not hand out secret:write. Offering it would be
    // offering a refusal.
    listMemberPermissions.mockResolvedValue(
      roster(["deploy:read", "deploy:write", "token:read", "token:write"])
    );
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");

    expect(
      screen.getByRole("checkbox", { name: /dev@example.test secret:write/i })
    ).toBeDisabled();
    expect(
      screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i })
    ).not.toBeDisabled();
  });

  it("explains itself when the caller cannot administer permissions at all", async () => {
    listMemberPermissions.mockResolvedValue(roster(["deploy:read"]));
    render(<MemberPermissions />);
    await screen.findByText(/dev@example.test/);
    expect(screen.getByText(/only.*view/i)).toBeInTheDocument();
  });

  it("surfaces a failed edit instead of showing the change as applied", async () => {
    updateMemberPermissions.mockRejectedValue(new Error("permission denied"));
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");

    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i }));

    expect(await screen.findByText(/could not update/i)).toBeInTheDocument();
    // And the checkbox must not be left looking ticked.
    await waitFor(() =>
      expect(
        screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i })
      ).not.toBeChecked()
    );
  });
});
