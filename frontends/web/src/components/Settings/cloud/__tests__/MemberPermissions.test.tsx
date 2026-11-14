import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useActiveOrgStore } from "@/store/activeOrgStore";

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
  listMemberPermissions: (orgId: string) => listMemberPermissions(orgId),
  updateMemberPermissions: (userId: string, scopes: string[], orgId: string) =>
    updateMemberPermissions(userId, scopes, orgId),
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
  "identity:read",
  "identity:manage",
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
    useActiveOrgStore.setState({ userId: "alice", chosenId: "org-a", effectiveId: "org-a" });
    listMemberPermissions.mockReset().mockResolvedValue(roster());
    updateMemberPermissions.mockReset().mockImplementation(
      (userId: string, scopes: string[]) =>
        Promise.resolve({ userId, email: "dev@example.test", role: "member", scopes })
    );
  });

  it("ignores an old org roster even after switching back to the same org", async () => {
    let finish!: (value: ReturnType<typeof roster>) => void;
    listMemberPermissions.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    render(<MemberPermissions />);
    await waitFor(() => expect(listMemberPermissions).toHaveBeenCalledTimes(1));
    const current = roster();
    current.members[1].email = "current@example.test";
    listMemberPermissions.mockResolvedValue(current);
    act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
    await screen.findByText("current@example.test");
    act(() => useActiveOrgStore.setState({ effectiveId: "org-a" }));
    await waitFor(() => expect(listMemberPermissions).toHaveBeenCalledTimes(3));
    await screen.findByText("current@example.test");
    await act(async () => finish(roster()));
    expect(screen.queryByText("dev@example.test")).toBeNull();
    expect(screen.getByText("current@example.test")).toBeInTheDocument();
  });

  it.each(["resolve", "reject"])("ignores a previous org edit %s without overwriting the new roster", async (outcome) => {
    let finish!: (value: unknown) => void;
    let fail!: (error: Error) => void;
    updateMemberPermissions.mockImplementationOnce(() => new Promise((resolve, reject) => { finish = resolve; fail = reject; }));
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");
    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i }));
    const current = roster();
    current.members[1].scopes = ["deploy:read", "secret:read"];
    listMemberPermissions.mockResolvedValue(current);
    act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /dev@example.test secret:read/i })).toBeChecked());
    await act(async () => {
      if (outcome === "resolve") finish({ scopes: ["deploy:write"] });
      else fail(new Error("old org refused"));
    });
    expect(screen.getByRole("checkbox", { name: /dev@example.test secret:read/i })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i })).not.toBeChecked();
    expect(screen.queryByText(/could not update/i)).toBeNull();
  });

  it.each(["resolve", "reject"])("ignores an older same-org edit %s after a newer edit", async (outcome) => {
    let finish!: (value: unknown) => void;
    let fail!: (error: Error) => void;
    updateMemberPermissions.mockImplementationOnce(() => new Promise((resolve, reject) => { finish = resolve; fail = reject; }));
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");
    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i }));
    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test secret:read/i }));
    await waitFor(() => expect(updateMemberPermissions).toHaveBeenCalledTimes(2));
    expect(updateMemberPermissions.mock.calls[1][2]).toBe("org-a");
    await act(async () => {
      if (outcome === "resolve") finish({ scopes: ["deploy:read", "deploy:write"] });
      else fail(new Error("old edit refused"));
    });
    expect(screen.getByRole("checkbox", { name: /dev@example.test secret:read/i })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /dev@example.test deploy:write/i })).toBeChecked();
    expect(screen.queryByText(/could not update/i)).toBeNull();
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
    const [userId, scopes, orgId] = updateMemberPermissions.mock.calls[0];
    expect(orgId).toBe("org-a");
    expect(listMemberPermissions).toHaveBeenCalledWith("org-a");
    expect(userId).toBe("u-dev");
    expect(scopes).toContain("deploy:write");
    expect(scopes).toContain("deploy:read");
  });

  it("offers the identity grants, which an admin hands out explicitly", async () => {
    // identity:manage is a per-org grant (control-plane orggrants.OrgScopes)
    // that deploy:write does not imply; without a box here no admin could
    // give it to a member.
    render(<MemberPermissions />);
    await screen.findByText("dev@example.test");

    fireEvent.click(screen.getByRole("checkbox", { name: /dev@example.test identity:manage/i }));

    await waitFor(() => expect(updateMemberPermissions).toHaveBeenCalledTimes(1));
    const [, scopes] = updateMemberPermissions.mock.calls[0];
    expect(scopes).toEqual(expect.arrayContaining(["deploy:read", "identity:manage"]));
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
