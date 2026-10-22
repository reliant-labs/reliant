/**
 * The CLI login consent page (`/oauth/cli`).
 *
 * Pins the three things the page is for: it keeps the CLI's request across a
 * sign-in detour, it forwards the request VERBATIM to the control plane with
 * the session JWT (the server re-validates; the page decides nothing), and it
 * hands the browser to whatever loopback URL the server returns.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const getSession = vi.fn();
vi.mock("../../../lib/supabase", () => ({
  supabase: { auth: { getSession: () => getSession() } },
}));
vi.mock("../../../lib/constants", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../lib/constants")>()),
  getControlPlaneURL: () => "https://admin.example.test",
}));

import { CliLogin } from "../CliLogin";

const QUERY =
  "?response_type=code&client_id=forge-cli&redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback" +
  "&state=st&code_challenge=abc&code_challenge_method=S256&scope=deploy%3Awrite&device=laptop";

let href = "";
beforeEach(() => {
  href = "";
  Object.defineProperty(window, "location", {
    configurable: true,
    value: {
      search: QUERY,
      get href() {
        return href;
      },
      set href(v: string) {
        href = v;
      },
    },
  });
});
afterEach(() => {
  vi.restoreAllMocks();
});

describe("CliLogin", () => {
  it("sends a signed-out user to /auth and back with the whole request", async () => {
    getSession.mockResolvedValue({ data: { session: null } });
    render(<CliLogin />);
    await waitFor(() => expect(href).not.toBe(""));
    expect(href).toBe(`/auth?redirect=${encodeURIComponent(`/oauth/cli${QUERY}`)}`);
  });

  it("forwards the request verbatim with the session JWT, then follows the server's redirect", async () => {
    getSession.mockResolvedValue({
      data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } },
    });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ redirect_to: "http://127.0.0.1:5000/callback?code=c1&state=st", scopes: ["deploy:write"] }),
        { status: 200 }
      )
    );
    render(<CliLogin />);
    expect(await screen.findByText("a@example.test")).toBeTruthy();
    expect(screen.getByTestId("cli-login-scopes").textContent).toContain("Deploy and promote");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(href).toBe("http://127.0.0.1:5000/callback?code=c1&state=st"));

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://admin.example.test/oauth/approve");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer jwt-1");
    expect(JSON.parse(init.body as string)).toEqual({ query: QUERY, approved: true });
  });

  it("reports a server refusal instead of navigating", async () => {
    getSession.mockResolvedValue({
      data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } },
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid_request", error_description: "bad redirect" }), { status: 400 })
    );
    render(<CliLogin />);
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect((await screen.findByTestId("cli-login-error")).textContent).toBe("bad redirect");
    expect(href).toBe("");
  });
});
