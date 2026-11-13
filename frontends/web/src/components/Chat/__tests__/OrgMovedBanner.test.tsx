import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MyOrg } from "@/services/controlPlane/orgs";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import { OrgMovedBanner, movedToOrg } from "../OrgMovedBanner";

const org = (id: string, name: string): MyOrg => ({ id, name, slug: id, role: "member", isOwner: false, isDefault: false });
const acme = org("acme", "Acme");
const globex = org("globex", "Globex");

const mock = vi.hoisted(() => ({ select: vi.fn(), activeOrgId: "acme" as string | null }));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    orgs: [acme, globex],
    active: mock.activeOrgId === "acme" ? acme : null,
    activeOrgId: mock.activeOrgId,
    select: mock.select,
  }),
}));

describe("movedToOrg", () => {
  const base = { chatOrgId: "globex", chatUserId: "me", meUserId: "me", activeOrgId: "acme", orgs: [acme, globex] };

  it("offers the chat's org when it is not the active one", () => {
    expect(movedToOrg(base)).toBe(globex);
  });
  it("says nothing for a chat in the active org, with no org, or before the list answers", () => {
    expect(movedToOrg({ ...base, chatOrgId: "acme" })).toBeNull();
    expect(movedToOrg({ ...base, chatOrgId: undefined })).toBeNull();
    expect(movedToOrg({ ...base, activeOrgId: null })).toBeNull();
  });
  it("says nothing to anyone but the chat's owner", () => {
    expect(movedToOrg({ ...base, chatUserId: "someone-else" })).toBeNull();
    expect(movedToOrg({ ...base, meUserId: null })).toBeNull();
  });
  it("says nothing for an org the caller is not a member of", () => {
    expect(movedToOrg({ ...base, chatOrgId: "initech" })).toBeNull();
  });
});

describe("OrgMovedBanner", () => {
  beforeEach(() => {
    mock.select.mockReset();
    mock.activeOrgId = "acme";
    useActiveOrgStore.setState({ userId: "me" });
  });

  it("tells the owner where the project went and switches the app there", () => {
    render(<OrgMovedBanner chatOrgId="globex" chatUserId="me" />);
    expect(screen.getByTestId("org-moved-banner")).toHaveTextContent("This project is now in Globex");
    fireEvent.click(screen.getByRole("button", { name: "Switch to Globex" }));
    expect(mock.select).toHaveBeenCalledWith("globex");
  });

  it("renders nothing for a chat in the active org", () => {
    render(<OrgMovedBanner chatOrgId="acme" chatUserId="me" />);
    expect(screen.queryByTestId("org-moved-banner")).toBeNull();
  });
});
