/**
 * Settings → Integrations: every connection, grouped by integration, with
 * what names it, and Rename / Make default / Test / Remove on the
 * ConnectionService's own writes.
 */
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  ApiKeyConnectionKind,
  ConnectionAuthKind,
  ConnectionSchema,
  ConnectionStatus,
  ListConnectionsResponseSchema,
} from "@/gen/reliant/v1/connection_pb";
import type { Trigger } from "@/hooks/trigger-model";

const mocks = vi.hoisted(() => ({
  triggers: [] as Trigger[],
  rename: vi.fn(),
  setDefault: vi.fn(),
  test: vi.fn(),
  remove: vi.fn(),
  startOAuth: vi.fn(),
  createKey: vi.fn(),
}));

vi.mock("@/hooks/trigger-queries", () => ({
  useTriggers: () => ({ data: mocks.triggers }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import {
  automationsByConnection,
  connectableIntegrations,
  groupConnections,
  IntegrationsSettings,
  usedBySentence,
} from "../IntegrationsSettings";

const oauth = { kind: ConnectionAuthKind.OAUTH2, available: true, unavailableReason: "", fieldLabels: {} };
const integrations = [
  { id: "github", displayName: "GitHub", methods: [oauth], connectionParams: [] },
  { id: "slack", displayName: "Slack", methods: [oauth], connectionParams: [] },
  {
    id: "twilio",
    displayName: "Twilio",
    methods: [{ kind: ConnectionAuthKind.BASIC, available: true, unavailableReason: "", fieldLabels: { username: "Account SID", password: "Auth Token" } }],
    connectionParams: [],
  },
  {
    id: "gmail",
    displayName: "Gmail",
    methods: [{ kind: ConnectionAuthKind.OAUTH2, available: false, unavailableReason: "not set", fieldLabels: {} }],
    connectionParams: [],
  },
];

const connections = [
  create(ConnectionSchema, { id: "c-gh", integrationId: "github", name: "Work GitHub", accountLabel: "acme-bot", status: ConnectionStatus.ACTIVE, isDefault: true }),
  create(ConnectionSchema, { id: "c-gh2", integrationId: "github", name: "Personal", accountLabel: "octocat", status: ConnectionStatus.ACTIVE }),
  create(ConnectionSchema, {
    id: "c-slack",
    integrationId: "slack",
    name: "Team Slack",
    status: ConnectionStatus.NEEDS_REAUTH,
    statusReason: "The token was revoked in Slack.",
    lastUsedAt: new Date(Date.now() - 3 * 60 * 60_000).toISOString(),
  }),
];

const trigger = (id: string, name: string, connectionId?: string) => ({ id, name, connectionId }) as Trigger;

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <IntegrationsSettings />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [mocks.rename, mocks.setDefault, mocks.test, mocks.remove, mocks.startOAuth, mocks.createKey]) fn.mockReset();
  mocks.startOAuth.mockResolvedValue("https://slack.com/oauth/v2/authorize?state=s");
  mocks.createKey.mockResolvedValue({});
  mocks.triggers = [trigger("t1", "PR review", "c-gh"), trigger("t2", "Nightly triage", "c-gh"), trigger("t3", "Alerts")];
  mocks.test.mockResolvedValue({ ok: true, probed: true, accountLabel: "acme-bot" });
  mocks.rename.mockResolvedValue({});
  mocks.setDefault.mockResolvedValue({});
  mocks.remove.mockResolvedValue({});
  setTransport(
    mockTransport({
      "ConnectionService.ListConnections": () => create(ListConnectionsResponseSchema, { connections }),
      "ConnectionService.ListIntegrations": () => ({ integrations }),
      "ConnectionService.StartOAuth": (req) => mocks.startOAuth(req).then((authorizeUrl: string) => ({ authorizeUrl })),
      "ConnectionService.CreateApiKeyConnection": (req) => mocks.createKey(req),
      "ConnectionService.RenameConnection": (req) => mocks.rename(req),
      "ConnectionService.SetDefaultConnection": (req) => mocks.setDefault(req),
      "ConnectionService.TestConnection": (req) => mocks.test(req),
      "ConnectionService.DeleteConnection": (req) => mocks.remove(req),
    }),
  );
});

describe("IntegrationsSettings", () => {
  it("lists connections by integration, with status and what names each", async () => {
    renderPage();
    const github = await screen.findByRole("list", { name: "GitHub connections" });
    const rows = within(github).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("Work GitHub");
    expect(within(rows[0]!).getByText("Default")).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent("Used by PR review and Nightly triage");
    expect(rows[1]).toHaveTextContent("No automation names it");
    const slack = screen.getByTestId("connection-c-slack");
    expect(within(slack).getByText("Needs sign-in again")).toBeInTheDocument();
    expect(within(slack).getByText("The token was revoked in Slack.")).toBeInTheDocument();
    expect(slack).toHaveTextContent("Last used 3 hours ago");
  });

  it("tests a connection against the live credential", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Test Work GitHub" }));
    await waitFor(() => expect(mocks.test).toHaveBeenCalledWith(expect.objectContaining({ id: "c-gh" })));
  });

  it("renames, and makes a connection the default", async () => {
    renderPage();
    const row = await screen.findByTestId("connection-c-gh2");
    await userEvent.click(within(row).getByRole("button", { name: "More actions for Personal" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Make default for GitHub" }));
    await waitFor(() => expect(mocks.setDefault).toHaveBeenCalledWith(expect.objectContaining({ id: "c-gh2" })));

    await userEvent.click(within(row).getByRole("button", { name: "More actions for Personal" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
    const input = within(row).getByRole("textbox", { name: "Name" });
    await userEvent.clear(input);
    await userEvent.type(input, "Side projects{Enter}");
    await waitFor(() => expect(mocks.rename).toHaveBeenCalledWith(expect.objectContaining({ id: "c-gh2", name: "Side projects" })));
  });

  it("removes only after a confirmation that names the automations losing it", async () => {
    renderPage();
    const row = await screen.findByTestId("connection-c-gh");
    await userEvent.click(within(row).getByRole("button", { name: "More actions for Work GitHub" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Remove…" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("2 automations name it (PR review, Nightly triage)");
    expect(mocks.remove).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove connection" }));
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(expect.objectContaining({ id: "c-gh" })));
  });

  it("offers Connect buttons when there are no connections", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.ListConnections": () => create(ListConnectionsResponseSchema, { connections: [] }),
        "ConnectionService.ListIntegrations": () => ({ integrations }),
      }),
    );
    renderPage();
    expect(await screen.findByText("No connections yet.")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Connect Slack" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect Twilio" })).toBeInTheDocument();
  });

  it("says nothing can be connected when the deployment lists no integrations", async () => {
    setTransport(
      mockTransport({
        "ConnectionService.ListConnections": () => create(ListConnectionsResponseSchema, { connections: [] }),
        "ConnectionService.ListIntegrations": () => ({ integrations: [] }),
      }),
    );
    renderPage();
    expect(await screen.findByText("No connections yet.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });
});

describe("IntegrationsSettings: Connect", () => {
  afterEach(() => {
    (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
    vi.unstubAllGlobals();
  });

  it("lists the integrations, with one this deployment cannot connect shown as unavailable", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    const list = await screen.findByRole("list", { name: "Integrations" });
    expect(within(list).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "GitHub",
      "Gmail" + "Not available on this deployment",
      "Slack",
      "Twilio",
    ]);
    expect(within(list).getByRole("button", { name: /Gmail/ })).toBeDisabled();
  });

  it("starts OAuth for a sign-in integration and returns to Settings → Integrations", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await userEvent.click(await screen.findByRole("button", { name: "Slack" }));
    await userEvent.click(await screen.findByRole("button", { name: /Continue to Slack/ }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://slack.com/oauth/v2/authorize?state=s"));
    expect(mocks.startOAuth).toHaveBeenCalledWith(expect.objectContaining({ integrationId: "slack", redirectAfter: "/settings/integrations" }));
  });

  it("desktop: waits for the system browser with a loopback redirect instead of leaving the page", async () => {
    const bridge = {
      startConnectionOAuthReceiver: vi.fn(async () => ({ flowId: "f1", redirectUri: "http://127.0.0.1:51000/callback" })),
      waitForProviderOAuth: vi.fn(() => new Promise(() => {})),
      cancelProviderOAuth: vi.fn(async () => undefined),
      openExternal: vi.fn(async () => undefined),
    };
    (window as unknown as { electronAPI?: unknown }).electronAPI = bridge;
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await userEvent.click(await screen.findByRole("button", { name: "Slack" }));
    await userEvent.click(await screen.findByRole("button", { name: /Continue to Slack/ }));

    await waitFor(() => expect(bridge.openExternal).toHaveBeenCalledWith("https://slack.com/oauth/v2/authorize?state=s"));
    expect(mocks.startOAuth).toHaveBeenCalledWith(expect.objectContaining({ loopbackRedirect: "http://127.0.0.1:51000/callback" }));
  });

  it("opens the credential form for a key integration and saves it with CreateApiKeyConnection", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await userEvent.click(await screen.findByRole("button", { name: "Twilio" }));

    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Account SID"), "AC123");
    await userEvent.type(within(dialog).getByLabelText("Auth Token"), "secret-token");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save connection" }));

    await waitFor(() =>
      expect(mocks.createKey).toHaveBeenCalledWith(
        expect.objectContaining({
          integrationId: "twilio",
          kind: ApiKeyConnectionKind.BASIC,
          fields: { username: "AC123", password: "secret-token" },
        }),
      ),
    );
    expect(mocks.startOAuth).not.toHaveBeenCalled();
  });

  it("adds another connection to an integration that already has one", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Add another Slack connection" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent("Connect Slack");
    expect(screen.getByRole("button", { name: /Continue to Slack/ })).toBeInTheDocument();
  });
});

describe("integration helpers", () => {
  it("offers only integrations that take a credential, by name", () => {
    const input = [
      { id: "z", displayName: "Zed", methods: [{ kind: "oauth2" as const, available: true, unavailableReason: "", fieldLabels: {} }], params: [] },
      { id: "n", displayName: "Nothing", methods: [{ kind: "none" as const, available: true, unavailableReason: "", fieldLabels: {} }], params: [] },
      { id: "a", displayName: "Alpha", methods: [{ kind: "api_key" as const, available: true, unavailableReason: "", fieldLabels: {} }], params: [] },
    ];
    expect(connectableIntegrations(input).map((i) => i.id)).toEqual(["a", "z"]);
  });

  it("groups by integration, default first", () => {
    const groups = groupConnections(connections.map((c) => ({ ...c, authKind: "oauth2", senderId: "", status: "active", statusReason: "", lastUsedAt: "", createdAt: "" })));
    expect(groups.map((g) => [g.integrationId, g.connections.map((c) => c.id)])).toEqual([
      ["github", ["c-gh", "c-gh2"]],
      ["slack", ["c-slack"]],
    ]);
  });

  it("counts the automations that name each connection", () => {
    const used = automationsByConnection(mocks.triggers);
    expect(used.get("c-gh")?.map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(usedBySentence([trigger("a", "A"), trigger("b", "B"), trigger("c", "C")])).toBe("Used by 3 automations");
  });
});
