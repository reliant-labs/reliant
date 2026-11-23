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
import { useAuthStore } from "@/store/authStore";
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

// forge sign-in is switched per test; everything else in forge-api is real.
const forge = vi.hoisted(() => ({ on: false }));
vi.mock("@/lib/auth/forge-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/forge-api")>()),
  isForgeAuth: () => forge.on,
}));

import { renderWithQuery } from "@/test/renderWithQuery";
import { forgeUserOf } from "@/lib/auth/forge-session";
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
  // AuthInitializer (App root) installs the session's user on every route,
  // and useMyOrgs fetches only for a signed-in user (#2669).
  useAuthStore.setState({ user: { id: "alice" } as never });
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
  forge.on = false;
  useAuthStore.setState({ initialized: false });
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

  it("starts on the org the CLI asked for (forge cloud login --org) when the person is in it", async () => {
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

  // `forge cloud login` asks for a human identity, which the control plane mints
  // bound to no org and refuses an org_id for ("human login takes no
  // organization"). The page must not offer a choice nor send one.
  it("sends no org and shows no picker for a human login (credential_kind=human)", async () => {
    listMyOrganizations.mockResolvedValue(TWO_ORGS);
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    const search = `${QUERY}&credential_kind=human`;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        search,
        get href() {
          return href;
        },
        set href(v: string) {
          href = v;
        },
      },
    });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ redirect_to: "http://127.0.0.1:5000/callback?code=c1&state=st" }), { status: 200 }),
    );
    renderWithQuery(<CliLogin />);
    const approve = await screen.findByRole("button", { name: "Approve" });
    expect(screen.queryByTestId("authorize-org")).toBeNull();
    expect(screen.queryByTestId("authorize-org-single")).toBeNull();
    await userEvent.click(approve);
    await waitFor(() => expect(href).toContain("code=c1"));
    expect(JSON.parse((fetchMock.mock.calls[0] as [string, RequestInit])[1].body as string)).toEqual({
      query: search,
      approved: true,
    });
  });

  describe("a third-party MCP client", () => {
    const RESOURCE = "https://api.example.test/mcp";
    // The query names the client "Totally Reliant": the page must never show it.
    const MCP_QUERY =
      "?response_type=code&client_id=mcp_abc&redirect_uri=https%3A%2F%2Fclaude.ai%2Fapi%2Fmcp%2Fauth_callback" +
      "&state=st&code_challenge=abc&code_challenge_method=S256&scope=openid" +
      `&resource=${encodeURIComponent(RESOURCE)}&client_name=Totally%20Reliant`;

    const setSearch = (search: string) =>
      Object.defineProperty(window, "location", {
        configurable: true,
        value: {
          search,
          get href() {
            return href;
          },
          set href(v: string) {
            href = v;
          },
        },
      });

    // Routes the client lookup and the approval to separate answers.
    const controlPlane = (client: () => Response) =>
      vi.spyOn(globalThis, "fetch").mockImplementation(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("https://admin.example.test/oauth/client?")) return client();
        return new Response(JSON.stringify({ redirect_to: "https://claude.ai/api/mcp/auth_callback?code=c1&state=st" }), {
          status: 200,
        });
      });

    beforeEach(() => {
      setSearch(MCP_QUERY);
      getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    });

    it("shows the control plane's name for the client, marked unverified, with the resource and what it may do", async () => {
      const fetchMock = controlPlane(
        () => new Response(JSON.stringify({ client_id: "mcp_abc", client_name: "Claude", third_party: true }), { status: 200 }),
      );
      renderWithQuery(<CliLogin />);

      expect((await screen.findByTestId("mcp-consent-client")).textContent).toBe("Claude");
      expect(screen.getByTestId("mcp-consent-unverified").textContent).toMatch(/Unverified app/);
      expect(screen.getByTestId("mcp-consent-unverified").textContent).toMatch(/registered itself automatically/);
      expect(screen.getByTestId("mcp-consent-resource").textContent).toBe(RESOURCE);
      expect(screen.getByTestId("mcp-consent-scopes").textContent).toContain(
        "Act as you on your Reliant MCP connectors",
      );
      expect(screen.getByText("claude.ai")).toBeTruthy();
      expect(document.body.textContent).not.toContain("Totally Reliant");
      expect(fetchMock.mock.calls[0][0]).toBe("https://admin.example.test/oauth/client?client_id=mcp_abc");

      const approve = screen.getByRole("button", { name: "Approve" });
      await waitFor(() => expect(approve).toBeEnabled());
      await userEvent.click(approve);
      await waitFor(() => expect(href).toBe("https://claude.ai/api/mcp/auth_callback?code=c1&state=st"));
      const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
      expect(url).toBe("https://admin.example.test/oauth/approve");
      expect(JSON.parse(init.body as string)).toEqual({ query: MCP_QUERY, approved: true, org_id: "org-a" });
    });

    it("says Reliant Forge, not workspace connectors, when the resource is the Reliant Forge connector", async () => {
      const forge = "https://api.example.test/forge/mcp";
      setSearch(MCP_QUERY.replace(encodeURIComponent(RESOURCE), encodeURIComponent(forge)));
      controlPlane(
        () => new Response(JSON.stringify({ client_id: "mcp_abc", client_name: "Claude", third_party: true }), { status: 200 }),
      );
      renderWithQuery(<CliLogin />);

      expect((await screen.findByTestId("mcp-consent-resource")).textContent).toBe(forge);
      const scopes = screen.getByTestId("mcp-consent-scopes").textContent ?? "";
      expect(scopes).toContain("Act as you in Reliant Forge: monitoring, releases and environments");
      expect(scopes).not.toContain("MCP connectors");
      expect(document.body.textContent).not.toContain("Which workspace it may use");
    });

    it.each([
      ["the control plane does not know it", () => new Response("{}", { status: 404 })],
      [
        "the control plane says it is not third-party",
        () => new Response(JSON.stringify({ client_id: "mcp_abc", client_name: "Claude", third_party: false }), { status: 200 }),
      ],
      [
        "the answer names another client",
        () => new Response(JSON.stringify({ client_id: "mcp_zzz", client_name: "Claude", third_party: true }), { status: 200 }),
      ],
      ["the lookup fails", () => new Response("not json", { status: 500 })],
    ])("offers no approval when %s", async (_why, answer) => {
      const fetchMock = controlPlane(answer);
      renderWithQuery(<CliLogin />);

      expect((await screen.findByTestId("cli-login-error")).textContent).toMatch(/cannot identify/);
      expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
      expect(document.body.textContent).not.toContain("Totally Reliant");
      expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith("/oauth/approve"))).toBe(false);
    });

    it("refuses a request with no client at all", async () => {
      setSearch(MCP_QUERY.replace("client_id=mcp_abc", "client_id="));
      const fetchMock = controlPlane(() => new Response("{}", { status: 404 }));
      renderWithQuery(<CliLogin />);
      expect((await screen.findByTestId("cli-login-error")).textContent).toMatch(/cannot identify/);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it("never looks up Reliant's own CLIs", async () => {
      setSearch(QUERY);
      const fetchMock = controlPlane(() => new Response("{}", { status: 404 }));
      renderWithQuery(<CliLogin />);
      expect(await screen.findByRole("button", { name: "Approve" })).toBeTruthy();
      expect(screen.queryByTestId("mcp-consent")).toBeNull();
      expect(fetchMock).not.toHaveBeenCalled();
    });
  });

  it("shows a person with one org that org in a sentence, not a choice", async () => {
    getSession.mockResolvedValue({ data: { session: { access_token: "jwt-1", user: { email: "a@example.test" } } } });
    renderWithQuery(<CliLogin />);
    expect(await screen.findByTestId("authorize-org-single")).toHaveTextContent("Acme");
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });
});

// AUTH_PROVIDER "forge": the broker's session cookie is the credential. The
// control plane reads it only on a request with NO Authorization header, and
// only same-origin, so the approval goes to this page's /_api/@cp.
describe("CliLogin with forge sign-in", () => {
  const ORIGIN = "https://app.example.test";
  beforeEach(() => {
    forge.on = true;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        origin: ORIGIN,
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

  it("approves same-origin with the session cookie and no Authorization header", async () => {
    useAuthStore.setState({
      user: forgeUserOf({ authenticated: true, subject: "alice", email: "a@forge.test" }),
      initialized: true,
    });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ redirect_to: "http://127.0.0.1:5000/callback?code=c1&state=st" }), { status: 200 }),
    );
    renderWithQuery(<CliLogin />);
    expect(await screen.findByText("a@forge.test")).toBeTruthy();
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => expect(href).toBe("http://127.0.0.1:5000/callback?code=c1&state=st"));

    const approve = fetchMock.mock.calls.find(([u]) => String(u).endsWith("/oauth/approve")) as [string, RequestInit];
    expect(approve[0]).toBe(`${ORIGIN}/_api/@cp/oauth/approve`);
    expect(approve[1].credentials).toBe("include");
    expect(new Headers(approve[1].headers).has("Authorization")).toBe(false);
    expect(JSON.parse(approve[1].body as string)).toEqual({ query: QUERY, approved: true, org_id: "org-a" });
    expect(getSession).not.toHaveBeenCalled();
  });

  it("sends a person with no broker session to /auth and back with the whole request", async () => {
    useAuthStore.setState({ user: null, initialized: true });
    renderWithQuery(<CliLogin />);
    await waitFor(() => expect(href).not.toBe(""));
    expect(href).toBe(`/auth?redirect=${encodeURIComponent(`/oauth/cli${QUERY}`)}`);
    expect(getSession).not.toHaveBeenCalled();
  });

  it("says the session ended when the control plane refuses the cookie", async () => {
    useAuthStore.setState({
      user: forgeUserOf({ authenticated: true, subject: "alice", email: "a@forge.test" }),
      initialized: true,
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("unauthorized", { status: 401 }));
    renderWithQuery(<CliLogin />);
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect((await screen.findByTestId("cli-login-error")).textContent).toBe(
      "Your session ended. Sign in and run the login again.",
    );
    expect(href).toBe("");
  });
});
