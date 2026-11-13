/**
 * Settings → Integrations, the active org's section: its connections listed
 * apart from the caller's own, read-only for a member, added and managed by
 * its admins as the org's (owner_org_id). While the deployment has not
 * enabled org connections, an add is refused with ORG_CONNECTIONS_DISABLED:
 * the section stops offering it and keeps the org's list.
 */
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  ApiKeyConnectionKind,
  ConnectionAuthKind,
  ConnectionOwnerKind,
  ConnectionSchema,
  ConnectionStatus,
  ListConnectionsResponseSchema,
} from "@/gen/reliant/v1/connection_pb";
import type { MyOrg } from "@/services/controlPlane/orgs";

const mocks = vi.hoisted(() => ({
  org: null as MyOrg | null,
  rename: vi.fn(),
  setDefault: vi.fn(),
  test: vi.fn(),
  remove: vi.fn(),
  startOAuth: vi.fn(),
  createKey: vi.fn(),
}));

vi.mock("@/hooks/trigger-queries", () => ({ useTriggers: () => ({ data: [] }) }));
vi.mock("@/hooks/useOrgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useOrgs")>()),
  useActiveOrg: () => ({ active: mocks.org, orgs: mocks.org ? [mocks.org] : [], activeOrgId: mocks.org?.id ?? null }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { IntegrationsSettings } from "../IntegrationsSettings";

const acme = (role: string): MyOrg => ({ id: "org-a", name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true });

const oauth = { kind: ConnectionAuthKind.OAUTH2, available: true, unavailableReason: "", fieldLabels: {} };
const integrations = [
  { id: "slack", displayName: "Slack", methods: [oauth], connectionParams: [] },
  {
    id: "twilio",
    displayName: "Twilio",
    methods: [{ kind: ConnectionAuthKind.BASIC, available: true, unavailableReason: "", fieldLabels: { username: "Account SID", password: "Auth Token" } }],
    connectionParams: [],
  },
];

const connections = [
  create(ConnectionSchema, { id: "c-mine", integrationId: "slack", name: "My Slack", status: ConnectionStatus.ACTIVE, isDefault: true }),
  create(ConnectionSchema, {
    id: "c-acme",
    integrationId: "slack",
    name: "Acme bot",
    status: ConnectionStatus.ACTIVE,
    isDefault: true,
    ownerKind: ConnectionOwnerKind.ORG,
    orgId: "org-a",
  }),
  // Another org's: never this section's.
  create(ConnectionSchema, { id: "c-other", integrationId: "slack", name: "Other org bot", status: ConnectionStatus.ACTIVE, ownerKind: ConnectionOwnerKind.ORG, orgId: "org-z" }),
];

const disabled = () =>
  new ConnectError("org connections are not enabled on this deployment", Code.FailedPrecondition, { "x-forge-error-reason": "org_connections_disabled" });

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
  mocks.org = acme("admin");
  mocks.startOAuth.mockResolvedValue("https://slack.com/oauth/v2/authorize?state=s");
  mocks.createKey.mockResolvedValue({});
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

const orgSection = () => screen.findByTestId("org-connections");

describe("IntegrationsSettings: the active org's connections", () => {
  it("lists the org's connections apart from the caller's own, and no other org's", async () => {
    renderPage();
    const section = await orgSection();
    expect(within(section).getByRole("heading", { name: "Acme's connections" })).toBeInTheDocument();
    expect(within(section).getByTestId("connection-c-acme")).toBeInTheDocument();
    expect(within(section).queryByTestId("connection-c-mine")).toBeNull();
    expect(screen.queryByTestId("connection-c-other")).toBeNull();
    // The personal list holds only the caller's own.
    const mine = screen.getByRole("list", { name: "Slack connections" });
    expect(within(mine).getByTestId("connection-c-mine")).toBeInTheDocument();
    expect(within(mine).queryByTestId("connection-c-acme")).toBeNull();
  });

  it("is read-only for a member: no Add, no row actions", async () => {
    mocks.org = acme("member");
    renderPage();
    const section = await orgSection();
    expect(section).toHaveTextContent("only its admins and owners add and manage them");
    expect(within(section).queryByTestId("org-connections-add")).toBeNull();
    const row = within(section).getByTestId("connection-c-acme");
    expect(within(row).queryByRole("button")).toBeNull();
    // Their own connections stay theirs to manage.
    expect(within(screen.getByTestId("connection-c-mine")).getByRole("button", { name: "Test My Slack" })).toBeInTheDocument();
  });

  it("shows no org section without an active org", async () => {
    mocks.org = null;
    renderPage();
    expect(await screen.findByTestId("connection-c-mine")).toBeInTheDocument();
    expect(screen.queryByTestId("org-connections")).toBeNull();
  });

  it("an admin adds a key connection as the org's", async () => {
    renderPage();
    await userEvent.click(within(await orgSection()).getByTestId("org-connections-add"));
    expect(await screen.findByRole("dialog", { name: "Connect an integration for Acme" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Twilio" }));

    const dialog = await screen.findByRole("dialog", { name: /Connect Twilio for Acme/ });
    await userEvent.type(within(dialog).getByLabelText("Account SID"), "AC123");
    await userEvent.type(within(dialog).getByLabelText("Auth Token"), "secret-token");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save connection" }));

    await waitFor(() =>
      expect(mocks.createKey).toHaveBeenCalledWith(expect.objectContaining({ integrationId: "twilio", kind: ApiKeyConnectionKind.BASIC, ownerOrgId: "org-a" })),
    );
  });

  it("an admin starts OAuth as the org's", async () => {
    // Desktop, so the flow waits on the system browser instead of navigating.
    const bridge = {
      startConnectionOAuthReceiver: vi.fn(async () => ({ flowId: "f1", redirectUri: "http://127.0.0.1:51000/callback" })),
      waitForProviderOAuth: vi.fn(() => new Promise(() => {})),
      cancelProviderOAuth: vi.fn(async () => undefined),
      openExternal: vi.fn(async () => undefined),
    };
    (window as unknown as { electronAPI?: unknown }).electronAPI = bridge;
    try {
      renderPage();
      await userEvent.click(within(await orgSection()).getByTestId("org-connections-add"));
      await userEvent.click(await screen.findByRole("button", { name: "Slack" }));
      expect(await screen.findByRole("dialog", { name: /Connect Slack for Acme/ })).toBeInTheDocument();
      await userEvent.click(await screen.findByRole("button", { name: /Continue to Slack/ }));

      await waitFor(() => expect(bridge.openExternal).toHaveBeenCalledWith("https://slack.com/oauth/v2/authorize?state=s"));
      expect(mocks.startOAuth).toHaveBeenCalledWith(expect.objectContaining({ integrationId: "slack", ownerOrgId: "org-a" }));
    } finally {
      (window as unknown as { electronAPI?: unknown }).electronAPI = undefined;
    }
  });

  it("a personal connect names no org", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await userEvent.click(await screen.findByRole("button", { name: "Twilio" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Account SID"), "AC123");
    await userEvent.type(within(dialog).getByLabelText("Auth Token"), "secret-token");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save connection" }));
    await waitFor(() => expect(mocks.createKey).toHaveBeenCalledTimes(1));
    expect((mocks.createKey.mock.calls[0]![0] as { ownerOrgId?: string }).ownerOrgId).toBeUndefined();
  });

  it("when the deployment refuses org connections, stops offering Add, says why, and keeps the list", async () => {
    mocks.createKey.mockRejectedValue(disabled());
    renderPage();
    await userEvent.click(within(await orgSection()).getByTestId("org-connections-add"));
    await userEvent.click(await screen.findByRole("button", { name: "Twilio" }));
    const dialog = await screen.findByRole("dialog", { name: /Connect Twilio for Acme/ });
    await userEvent.type(within(dialog).getByLabelText("Account SID"), "AC123");
    await userEvent.type(within(dialog).getByLabelText("Auth Token"), "secret-token");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save connection" }));

    expect(await within(dialog).findByText("Adding connections for Acme isn't enabled on this deployment yet.")).toBeInTheDocument();
    const section = screen.getByTestId("org-connections");
    expect(within(section).getByTestId("org-connections-add")).toBeDisabled();
    expect(within(section).getByTestId("org-connections-disabled")).toHaveTextContent("isn't enabled on this deployment yet");
    expect(within(section).getByTestId("connection-c-acme")).toBeInTheDocument();
  });

  it("acts on an org row as the org's", async () => {
    renderPage();
    const row = within(await orgSection()).getByTestId("connection-c-acme");
    await userEvent.click(within(row).getByRole("button", { name: "Test Acme bot" }));
    await waitFor(() => expect(mocks.test).toHaveBeenCalledWith(expect.objectContaining({ id: "c-acme", ownerOrgId: "org-a" })));

    await userEvent.click(within(row).getByRole("button", { name: "More actions for Acme bot" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
    const input = within(row).getByRole("textbox", { name: "Name" });
    await userEvent.clear(input);
    await userEvent.type(input, "Acme alerts{Enter}");
    await waitFor(() => expect(mocks.rename).toHaveBeenCalledWith(expect.objectContaining({ id: "c-acme", name: "Acme alerts", ownerOrgId: "org-a" })));

    await userEvent.click(within(row).getByRole("button", { name: "More actions for Acme bot" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Remove…" }));
    const confirm = await screen.findByRole("dialog");
    expect(confirm).toHaveTextContent("Acme's default Slack connection");
    await userEvent.click(within(confirm).getByRole("button", { name: "Remove connection" }));
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith(expect.objectContaining({ id: "c-acme", ownerOrgId: "org-a" })));
  });

  it("acts on a personal row naming no org", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Test My Slack" }));
    await waitFor(() => expect(mocks.test).toHaveBeenCalledTimes(1));
    expect((mocks.test.mock.calls[0]![0] as { ownerOrgId?: string }).ownerOrgId).toBeUndefined();
  });
});
