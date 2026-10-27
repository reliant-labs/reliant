/**
 * The page `reliant daemon start` links to (`/oauth/device?user_code=…`).
 *
 * Pins: a signed-out person is sent to sign in and back with the code kept;
 * the code in the link is looked up and the prompt names the machine; the
 * answer goes to the control plane with the session JWT.
 */
import { render, screen, waitFor } from "@testing-library/react";
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

import { DevicePairing } from "../DevicePairing";

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
});
afterEach(() => {
  vi.unstubAllGlobals();
});

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

describe("DevicePairing", () => {
  it("sends a signed-out person to sign in and back with the code", async () => {
    getSession.mockResolvedValue({ data: { session: null } });
    render(<DevicePairing />);
    await waitFor(() => expect(href).toBe(`/auth?redirect=${encodeURIComponent("/oauth/device?user_code=BDFG-HJKL")}`));
  });

  it("asks to approve the machine the code names, and records the answer with the session", async () => {
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1" } } });
    fetchMock
      .mockResolvedValueOnce(
        ok({ user_code: "BDFG-HJKL", hostname: "build-box", platform: "linux/amd64", client_id: "reliant-cli", scopes: [], expires_at: "" }),
      )
      .mockResolvedValueOnce(ok({ approved: true, hostname: "build-box" }));

    render(<DevicePairing />);
    expect(await screen.findByText("Approve machine build-box?")).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe("https://admin.example.test/oauth/device/lookup");
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ user_code: "BDFGHJKL" });

    await userEvent.setup().click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Machine approved")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe("https://admin.example.test/oauth/device/approve");
    expect(init.headers.Authorization).toBe("Bearer jwt-1");
    expect(JSON.parse(init.body)).toEqual({ user_code: "BDFGHJKL", approved: true });
  });
});
