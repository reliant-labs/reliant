// Copyright (c) 2025 Reliant Labs

/**
 * An environment link that names another org (the control plane's queued-
 * deploy action URL, `…?forgeProject=…&org=<id>`): the page reads the control
 * plane in the ACTIVE org, so it asks before switching, reads nothing until
 * the person has chosen, and never switches on its own.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ActiveOrg } from "@/hooks/useOrgs";

const navigate = vi.fn();
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => navigate,
  useParams: () => ({ env: "prod" }),
  useSearch: () => ({ org: "org-b" }),
}));

// The environment's control-plane read: it must not go out in the wrong org.
const useLiveView = vi.fn(() => {
  throw new Error("the environment was read before the person chose an org");
});
vi.mock("@/hooks/forge-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/forge-queries")>()),
  useLiveView: () => useLiveView(),
}));

const select = vi.fn();
const org = (id: string, name: string) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault: false });
const ORGS = [org("org-a", "Acme"), org("org-b", "Side Project")];
const active: Partial<ActiveOrg> = { orgs: ORGS, active: ORGS[0], activeOrgId: "org-a" };
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    defaultOrgId: "",
    canSwitch: true,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    select,
    ...active,
  }),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { ForgeEnvPage } from "../ForgeEnvPage";

beforeEach(() => {
  navigate.mockReset();
  select.mockReset();
  useLiveView.mockClear();
});

describe("ForgeEnvPage, opened from a link for another org", () => {
  it("asks before switching, and reads nothing meanwhile", () => {
    render(<ForgeEnvPage />);
    const gate = screen.getByTestId("env-org-gate");
    expect(gate).toHaveTextContent("This environment link is for Side Project");
    expect(gate).toHaveTextContent("You're acting in Acme");
    expect(screen.queryByTestId("forge-env-page")).toBeNull();
    expect(useLiveView).not.toHaveBeenCalled();
    expect(select).not.toHaveBeenCalled();
  });

  it("switches when asked and drops the link's org from the URL", async () => {
    render(<ForgeEnvPage />);
    await userEvent.click(screen.getByTestId("env-org-switch"));
    expect(select).toHaveBeenCalledWith("org-b");
    const call = navigate.mock.calls.at(-1)?.[0] as { to: string; replace: boolean; search: (p: Record<string, unknown>) => Record<string, unknown> };
    expect(call).toMatchObject({ to: ".", replace: true });
    expect(call.search({ env: "prod", org: "org-b" })).toEqual({ env: "prod", org: undefined });
  });

  it("staying drops the link's org and keeps the active one", async () => {
    render(<ForgeEnvPage />);
    await userEvent.click(screen.getByTestId("env-org-stay"));
    expect(select).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: ".", replace: true }));
  });
});
