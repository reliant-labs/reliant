/**
 * The CLI login consent page (`/oauth/cli`).
 *
 * Pins the three things the page is for: it keeps the CLI's request across a
 * sign-in detour, it forwards the request VERBATIM to the control plane with
 * the session JWT (the server re-validates; the page decides nothing), and it
 * hands the browser to whatever loopback URL the server returns.
 */

import { act, screen, waitFor } from "@testing-library/react";
import { useActiveOrgStore } from "@/store/activeOrgStore";
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

import { renderWithQuery } from "@/test/renderWithQuery";
import { CliLogin } from "../CliLogin";

const listMyOrganizations = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://admin.example.test", hasControlPlane: true }));

const org = (id: string, name: string, isDefault = false) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault });
const ONE_ORG = { orgs: [org("org-a", "Acme", true)], defaultOrgId: "org-a" };
const TWO_ORGS = { orgs: [org("org-a", "Acme", true), org("org-b", "Side Project")], defaultOrgId: "org-a" };

const QUERY =
  "?response_type=code&client_id=forge-cli&redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback" +
  "&state=st&code_challenge=abc&code_challenge_method=S256&scope=deploy%3Awrite&device=laptop";

let href = "";
beforeEach(() => {
  listMyOrganizations.mockResolvedValue(ONE_ORG);
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
  it("retains the approved org while session authentication waits across an app-org switch", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt", user: { email: "a@test" } } } });
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ redirect_to: "http://127.0.0.1/callback", scopes: [] }) });
    vi.stubGlobal("fetch", fetchMock);
    renderWithQuery(<CliLogin />);
    const approve = await screen.findByRole("button", { name: /approve/i });
    await waitFor(() => expect(approve).toBeEnabled());
    let finish!: (value: unknown) => void;
    getSession.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    await userEvent.click(approve);
    act(() => useActiveOrgStore.setState({ effectiveId: "org-b" }));
    await act(async () => finish({ data: { session: { access_token: "jwt" } } }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).org_id).toBe("org-a");
    vi.unstubAllGlobals();
  });

  it("sends a signed-out user to /auth and back with the whole request", async () => {
    getSession.mockResolvedValue({ data: { session: null } });
    renderWithQuery(<CliLogin />);
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
    renderWithQuery(<CliLogin />);
    expect(await screen.findByText("a@example.test")).toBeTruthy();
    expect(screen.getByTestId("cli-login-scopes").textContent).toContain("Deploy and promote");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(href).toBe("http://127.0.0.1:5000/callback?code=c1&state=st"));

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://admin.example.test/oauth/approve");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer jwt-1");
    expect(JSON.parse(init.body as string)).toEqual({ query: QUERY, approved: true, org_id: "org-a" });
  });

  it("reports a server refusal instead of navigating", async () => {
    getSession.mockResolvedValue({
      data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } },
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid_request", error_description: "bad redirect" }), { status: 400 })
    );
    renderWithQuery(<CliLogin />);
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect((await screen.findByTestId("cli-login-error")).textContent).toBe("bad redirect");
    expect(href).toBe("");
  });

  it("lets the person pick which org the login acts in, and sends that choice", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ redirect_to: "http://127.0.0.1:5000/callback?code=c1", org_id: "org-b" }), { status: 200 }),
    );
    renderWithQuery(<CliLogin />);
    const picker = await screen.findByTestId("authorize-org");
    // Starts on the default org, which is what a login with no org asked for gets.
    expect(screen.getByRole("radio", { name: /Acme/ })).toHaveAttribute("aria-checked", "true");

    await userEvent.click(screen.getByRole("radio", { name: /Side Project/ }));
    expect(picker.textContent).toContain("Side Project");
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(href).toContain("code=c1"));
    expect(JSON.parse((fetchMock.mock.calls[0] as [string, RequestInit])[1].body as string).org_id).toBe("org-b");
  });

  it("starts on the org the CLI asked for (forge login --org) when the person is in it", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { search: `${QUERY}&org=org-b`, href: "" },
    });
    renderWithQuery(<CliLogin />);
    expect(await screen.findByRole("radio", { name: /Side Project/ })).toHaveAttribute("aria-checked", "true");
  });

  it("ignores an org the CLI asked for that the person is not in, and starts on their default", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { search: `${QUERY}&org=org-zzz`, href: "" },
    });
    renderWithQuery(<CliLogin />);
    expect(await screen.findByRole("radio", { name: /Acme/ })).toHaveAttribute("aria-checked", "true");
  });

  it("shows a person with one org that org in a sentence, not a choice", async () => {
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    renderWithQuery(<CliLogin />);
    expect(await screen.findByTestId("authorize-org-single")).toHaveTextContent("Acme");
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });
});
