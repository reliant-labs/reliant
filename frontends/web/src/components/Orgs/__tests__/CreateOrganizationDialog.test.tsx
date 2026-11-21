// Copyright (c) 2025 Reliant Labs

/**
 * After creating an organization the dialog offers the next step — app
 * hosting billing, which a new org needs for a backend or a second site —
 * and never blocks on it: "Not now" closes with the members hint, and the
 * org exists either way.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

const createOrganization = vi.fn();
vi.mock("@/services/controlPlane/members", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/members")>()),
  createOrganization: (name: string) => createOrganization(name),
}));
const switchToJoinedOrg = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  switchToJoinedOrg: (id: string) => switchToJoinedOrg(id),
}));
const toastSuccess = vi.fn();
vi.mock("@/lib/toast-manager", () => ({ toast: { success: (m: string) => toastSuccess(m), error: vi.fn() } }));

import { CreateOrganizationDialog } from "../CreateOrganizationDialog";

async function create(onClose = vi.fn()) {
  render(<CreateOrganizationDialog open onClose={onClose} />);
  await userEvent.type(screen.getByTestId("create-org-name"), "New Co");
  await userEvent.click(screen.getByTestId("create-org-submit"));
  await screen.findByTestId("create-org-next-step");
  return onClose;
}

beforeEach(() => {
  navigate.mockReset();
  toastSuccess.mockReset();
  createOrganization.mockReset().mockResolvedValue({ id: "org-new", name: "New Co" });
  switchToJoinedOrg.mockReset().mockResolvedValue(undefined);
});

describe("CreateOrganizationDialog", () => {
  it("creates, switches, and offers hosting billing as the next step instead of closing", async () => {
    const onClose = await create();
    expect(createOrganization).toHaveBeenCalledWith("New Co");
    expect(switchToJoinedOrg).toHaveBeenCalledWith("org-new");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByText("Created New Co")).toBeInTheDocument();
    expect(screen.getByText(/set up app hosting billing, so this organization can host backends/)).toBeInTheDocument();
  });

  it("Set up hosting billing opens the new org's hosting tab, then closes", async () => {
    const onClose = await create();
    await userEvent.click(screen.getByTestId("create-org-hosting-billing"));
    expect(navigate).toHaveBeenCalledWith({
      to: "/settings/billing",
      search: { tab: "hosting", org: "org-new" },
      hash: "app-hosting",
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Not now closes with the members hint, and goes nowhere", async () => {
    const onClose = await create();
    await userEvent.click(screen.getByTestId("create-org-not-now"));
    expect(navigate).not.toHaveBeenCalled();
    expect(toastSuccess).toHaveBeenCalledWith("Created New Co. Invite people to it from Settings › Members.");
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });
});
