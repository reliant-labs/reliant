/**
 * The page `reliant daemon start` links to (`/oauth/device?user_code=…`).
 *
 * Pins: a signed-out person is sent to sign in and back with the code kept;
 * the code in the link is looked up and the prompt names the machine; the
 * answer goes to the control plane with the session JWT.
 */
import { act, screen, waitFor } from "@testing-library/react";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const getSession = vi.fn();
vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: () => getSession() } },
}));
vi.mock("@/lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/constants")>()),
  getControlPlaneURL: () => "https://admin.example.test",
}));

import { renderWithQuery } from "@/test/renderWithQuery";
import { DevicePairing } from "../DevicePairing";

const listMyOrganizations = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://admin.example.test", hasControlPlane: true }));

const org = (id: string, name: string, isDefault = false) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault });
const TWO_ORGS = { orgs: [org("org-a", "Acme", true), org("org-b", "Side Project")], defaultOrgId: "org-a" };

let href = "";
const fetchMock = vi.fn();
beforeEach(() => {
  href = "";
  Object.defineProperty(window, "location", {
    configurable: true,
    value: {
      search: "?user_code=BDFG-HJKL",
      get href() {
        return href;
      },
      set href(v: string) {
        href = v;
      },
    },
  });
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  listMyOrganizations.mockResolvedValue({ orgs: [org("org-a", "Acme", true)], defaultOrgId: "org-a" });
});
afterEach(() => {
  vi.unstubAllGlobals();
});

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

describe("DevicePairing", () => {
  it("keeps the confirmed machine org across deferred session authentication", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt" } } });
    fetchMock.mockResolvedValueOnce(ok({ user_code: "BDFG-HJKL", hostname: "box", scopes: [] }))
      .mockResolvedValueOnce(ok({ approved: true }));
    renderWithQuery(<DevicePairing />);
    await screen.findByText("Approve machine box?");
    let finish!: (value: unknown) => void;
    getSession.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
    await act(async () => finish({ data: { session: { access_token: "jwt" } } }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).org_id).toBe("org-a");
  });

  it("sends a signed-out person to sign in and back with the code", async () => {
    getSession.mockResolvedValue({ data: { session: null } });
    renderWithQuery(<DevicePairing />);
    await waitFor(() => expect(href).toBe(`/auth?redirect=${encodeURIComponent("/oauth/device?user_code=BDFG-HJKL")}`));
  });

  it("asks to approve the machine the code names, and records the answer with the session", async () => {
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1" } } });
    fetchMock
      .mockResolvedValueOnce(
        ok({ user_code: "BDFG-HJKL", hostname: "build-box", platform: "linux/amd64", client_id: "reliant-cli", scopes: [], expires_at: "" }),
      )
      .mockResolvedValueOnce(ok({ approved: true, hostname: "build-box" }));

    renderWithQuery(<DevicePairing />);
    expect(await screen.findByText("Approve machine build-box?")).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe("https://admin.example.test/oauth/device/lookup");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ user_code: "BDFGHJKL" });

    await userEvent.setup().click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Machine approved")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe("https://admin.example.test/oauth/device/approve");
    expect(init.headers.Authorization).toBe("Bearer jwt-1");
    expect(JSON.parse(init.body)).toEqual({ user_code: "BDFGHJKL", approved: true, org_id: "org-a" });
  });

  it("asks which org the machine will act in, and approves into the one picked", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1" } } });
    fetchMock
      .mockResolvedValueOnce(
        ok({ user_code: "BDFG-HJKL", hostname: "build-box", platform: "linux/amd64", client_id: "reliant-cli", scopes: [], expires_at: "" }),
      )
      .mockResolvedValueOnce(ok({ approved: true }));

    renderWithQuery(<DevicePairing />);
    expect(await screen.findByTestId("authorize-org")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("radio", { name: /Side Project/ }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Approve" }));
    await screen.findByText("Machine approved");
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ user_code: "BDFGHJKL", approved: true, org_id: "org-b" });
  });

  it("names no org when declining", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1" } } });
    fetchMock
      .mockResolvedValueOnce(
        ok({ user_code: "BDFG-HJKL", hostname: "build-box", platform: "linux/amd64", client_id: "reliant-cli", scopes: [], expires_at: "" }),
      )
      .mockResolvedValueOnce(ok({}));
    renderWithQuery(<DevicePairing />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Not my machine" }));
    await screen.findByText("Machine declined");
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ user_code: "BDFGHJKL", approved: false });
  });
});
