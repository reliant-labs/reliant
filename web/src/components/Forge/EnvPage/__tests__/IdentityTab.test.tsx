// Copyright (c) 2025 Reliant Labs

/**
 * The Identity tab, in every state a reader can meet it in, against an
 * in-memory IdentityApi (the same seam production fills with the real client).
 *
 * The two that matter most:
 *   - NOT ENABLED. Prod answers Unavailable until the reconciler's internal API
 *     is switched on. That is a designed state — never a toast, never a retry.
 *   - A MEMBER sees no control they would be refused. The server checks again;
 *     this pins that the console does not offer the action in the first place.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { IdentityApiProvider, type IdentityApi } from "@/services/controlPlane/identity/IdentityApiContext";
import type { IdentityStatus, IdentityUsersPage } from "@/services/controlPlane/identity/api";
import type { LiveEnv } from "@/services/forge/live";

import { IdentityTab } from "../IdentityTab";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

const NOW = Date.now();
const LIVE = { id: "env-prod", name: "prod" } as LiveEnv;

function status(overrides: Partial<IdentityStatus> = {}): IdentityStatus {
  return {
    hasInstance: true,
    state: "ready",
    issuer: "https://acme-prod-3fa9c1.reliantidentity.com",
    domain: "acme-prod-3fa9c1.reliantidentity.com",
    public: false,
    adopted: false,
    apps: [{ name: "prod:api", clientId: "4061000", brokerExpiresAtMs: NOW + 21 * 86_400_000 }],
    deliveries: [
      {
        kind: "identity",
        cluster: "acme-prod",
        namespace: "acme",
        name: "forge-identity-identity",
        state: "delivered",
        lastError: "",
        lastVerifiedAtMs: NOW - 120_000,
      },
    ],
    ownerEnvironmentId: "env-prod",
    sharerEnvironmentIds: ["env-preview"],
    lastReconciledAtMs: NOW - 60_000,
    nextAttemptAtMs: null,
    lastError: "",
    failures: 0,
    graceUntilMs: null,
    exportUrlAvailable: false,
    refused: "",
    callerCanManage: true,
    exportEnabled: false,
    mailDelivery: "smtp",
    ...overrides,
  };
}

const USERS: IdentityUsersPage = {
  users: [
    { userId: "u-alice", email: "alice@acme.test", displayName: "Alice", state: "active", roles: ["operator"], createdAtMs: NOW },
    { userId: "u-bob", email: "bob@acme.test", displayName: "", state: "locked", roles: ["admin"], createdAtMs: NOW },
  ],
  nextPageToken: "",
  roles: [
    { key: "admin", displayName: "Administrator", group: "", declared: true },
    { key: "operator", displayName: "Operator", group: "", declared: false },
    { key: "viewer", displayName: "Viewer", group: "", declared: false },
  ],
};

function fakeApi(overrides: Partial<IdentityApi> = {}): IdentityApi {
  return {
    currentOrgId: vi.fn(async () => "org-a"),
    getStatus: vi.fn(async () => status()),
    listUsers: vi.fn(async () => USERS),
    inviteUser: vi.fn(async () => ({ userId: "u-new", created: true, code: null })),
    setUserRoles: vi.fn(async () => null),
    sendPasswordReset: vi.fn(async () => null),
    lockUser: vi.fn(async () => {}),
    unlockUser: vi.fn(async () => {}),
    deleteUser: vi.fn(async () => {}),
    revokeSessions: vi.fn(async () => 2),
    getExportURL: vi.fn(async () => ({ url: "https://signed.example/x", expiresAtMs: NOW + 600_000 })),
    ...overrides,
  };
}

function renderTab(api: IdentityApi, props: Partial<Parameters<typeof IdentityTab>[0]> = {}) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <IdentityApiProvider api={api}>
        <IdentityTab
          envName="prod"
          lifecycle="deployed"
          live={LIVE}
          liveLoading={false}
          envNameById={(id) => ({ "env-prod": "prod", "env-preview": "preview-142" })[id]}
          {...props}
        />
      </IdentityApiProvider>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  for (const fn of Object.values(toasts)) fn.mockReset();
});

describe("IdentityTab", () => {
  it("shows the status, the users and the admin controls when identity is enabled", async () => {
    const api = fakeApi();
    renderTab(api);

    expect(await screen.findByTestId("identity-issuer")).toHaveTextContent("https://acme-prod-3fa9c1.reliantidentity.com");
    expect(screen.getByTestId("identity-visibility")).toHaveTextContent(/Private/);
    expect(screen.getByTestId("identity-apps")).toHaveTextContent("4061000");
    expect(screen.getByTestId("identity-apps")).toHaveTextContent(/renews in 21d/);
    expect(screen.getByTestId("identity-deliveries")).toHaveTextContent("acme-prod · acme/forge-identity-identity");
    expect(screen.getByTestId("identity-sharing")).toHaveTextContent("preview-142");

    const alice = await screen.findByTestId("identity-user-u-alice");
    expect(alice).toHaveTextContent("Active");
    expect(screen.getByTestId("identity-user-u-bob")).toHaveTextContent("Locked");
    expect(within(alice).getByTestId("identity-user-actions")).toBeInTheDocument();
    expect(screen.getByTestId("identity-invite")).toBeInTheDocument();
    expect(screen.queryByTestId("identity-read-only")).not.toBeInTheDocument();
    expect(api.listUsers).toHaveBeenCalledWith("org-a", "env-prod", { query: "", state: undefined }, "");
  });

  it("renders Unavailable as 'not enabled yet', with no toast and no retry", async () => {
    const getStatus = vi.fn(async () => {
      throw new ConnectError("identity management is not configured on this server", Code.Unavailable);
    });
    renderTab(fakeApi({ getStatus }));

    expect(await screen.findByTestId("identity-not-enabled")).toHaveTextContent(
      "Managed identity isn't enabled for this environment yet"
    );
    expect(screen.queryByTestId("identity-error")).not.toBeInTheDocument();
    for (const fn of Object.values(toasts)) expect(fn).not.toHaveBeenCalled();
    expect(getStatus).toHaveBeenCalledTimes(1);
  });

  it("shows a skeleton while the status loads", () => {
    renderTab(fakeApi({ getStatus: () => new Promise<IdentityStatus>(() => {}) }));
    expect(screen.getByTestId("identity-loading")).toHaveAttribute("aria-busy", "true");
  });

  it("shows the server's words and a retry for any other failure", async () => {
    let fail = true;
    const getStatus = vi.fn(async () => {
      if (fail) throw new ConnectError("relay exploded", Code.Internal);
      return status();
    });
    renderTab(fakeApi({ getStatus }));

    // An unexpected failure is retried once (identityRetry) before it is shown.
    const error = await screen.findByTestId("identity-error", {}, { timeout: 4000 });
    expect(error).toHaveTextContent("relay exploded");
    expect(getStatus).toHaveBeenCalledTimes(2);
    fail = false;
    await userEvent.click(within(error).getByRole("button", { name: "Try again" }));
    expect(await screen.findByTestId("identity-tab")).toBeInTheDocument();
  });

  it("gives a member no control they would be refused", async () => {
    const api = fakeApi({ getStatus: vi.fn(async () => status({ callerCanManage: false })) });
    renderTab(api);

    expect(await screen.findByTestId("identity-read-only")).toHaveTextContent(/needs an organization admin/);
    await screen.findByTestId("identity-user-u-alice");
    expect(screen.queryByTestId("identity-user-actions")).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-invite")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete/i })).not.toBeInTheDocument();
  });

  it("shows declared roles read-only, managed in deploy/kcl/identity.k", async () => {
    renderTab(fakeApi());
    const alice = await screen.findByTestId("identity-user-u-alice");
    await userEvent.click(within(alice).getByRole("button", { name: "Roles" }));

    const editor = screen.getByTestId("identity-role-editor");
    const declared = within(editor).getByTestId("identity-role-admin");
    expect(within(declared).getByRole("checkbox")).toBeDisabled();
    expect(declared).toHaveTextContent("managed in deploy/kcl/identity.k");
    expect(within(within(editor).getByTestId("identity-role-operator")).getByRole("checkbox")).toBeChecked();
    expect(within(within(editor).getByTestId("identity-role-viewer")).getByRole("checkbox")).toBeEnabled();

    // Bob holds the declared role: deleting him would be undone by the next reconcile.
    const bob = screen.getByTestId("identity-user-u-bob");
    expect(within(bob).getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("sends the complete console-managed set when roles are saved", async () => {
    const api = fakeApi();
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");
    await userEvent.click(within(alice).getByRole("button", { name: "Roles" }));
    const editor = screen.getByTestId("identity-role-editor");
    await userEvent.click(within(within(editor).getByTestId("identity-role-viewer")).getByRole("checkbox"));
    await userEvent.click(within(editor).getByRole("button", { name: "Save roles" }));

    await waitFor(() =>
      expect(api.setUserRoles).toHaveBeenCalledWith("org-a", "env-prod", { userId: "u-alice", roles: ["operator", "viewer"] })
    );
  });

  it("asks before a destructive action, and does nothing on cancel", async () => {
    const api = fakeApi();
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");

    await userEvent.click(within(alice).getByRole("button", { name: "Delete" }));
    const dialog = screen.getByRole("dialog", { name: "Delete alice@acme.test?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(api.deleteUser).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await userEvent.click(within(alice).getByRole("button", { name: "Delete" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete user" }));
    await waitFor(() => expect(api.deleteUser).toHaveBeenCalledWith("org-a", "env-prod", "u-alice"));
    expect(toasts.success).toHaveBeenCalledWith("alice@acme.test was deleted");
  });

  it("confirms lock and end-sessions too, but sends a reset and an unlock at once", async () => {
    const api = fakeApi();
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");

    await userEvent.click(within(alice).getByRole("button", { name: "Lock" }));
    expect(api.lockUser).not.toHaveBeenCalled();
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Lock user" }));
    await waitFor(() => expect(api.lockUser).toHaveBeenCalledWith("org-a", "env-prod", "u-alice"));

    await userEvent.click(within(alice).getByRole("button", { name: "End sessions" }));
    expect(api.revokeSessions).not.toHaveBeenCalled();
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "End sessions" }));
    await waitFor(() => expect(api.revokeSessions).toHaveBeenCalledWith("org-a", "env-prod", "u-alice"));

    await userEvent.click(within(alice).getByRole("button", { name: "Email reset" }));
    await waitFor(() => expect(api.sendPasswordReset).toHaveBeenCalledWith("org-a", "env-prod", "u-alice"));
    await userEvent.click(within(screen.getByTestId("identity-user-u-bob")).getByRole("button", { name: "Unlock" }));
    await waitFor(() => expect(api.unlockUser).toHaveBeenCalledWith("org-a", "env-prod", "u-bob"));
  });

  it("invites with an email and console-managed roles only", async () => {
    const api = fakeApi();
    renderTab(api);
    const invite = await screen.findByTestId("identity-invite");
    await screen.findByTestId("identity-user-u-alice");

    expect(within(within(invite).getByTestId("identity-role-admin")).getByRole("checkbox")).toBeDisabled();
    const send = within(invite).getByRole("button", { name: "Send invitation" });
    expect(send).toBeDisabled();
    await userEvent.type(within(invite).getByTestId("identity-invite-email"), "carol@acme.test");
    await userEvent.click(within(within(invite).getByTestId("identity-role-viewer")).getByRole("checkbox"));
    await userEvent.click(send);

    await waitFor(() =>
      expect(api.inviteUser).toHaveBeenCalledWith("org-a", "env-prod", { email: "carol@acme.test", roles: ["viewer"], returnCode: false })
    );
    expect(toasts.success).toHaveBeenCalledWith("Emailed carol@acme.test an invitation");
  });

  it("searches users by email", async () => {
    const api = fakeApi();
    renderTab(api);
    await screen.findByTestId("identity-user-u-alice");
    await userEvent.type(screen.getByTestId("identity-users-search"), "bob");
    await waitFor(() =>
      expect(api.listUsers).toHaveBeenLastCalledWith("org-a", "env-prod", { query: "bob", state: undefined }, "")
    );
  });

  it("says export is not enabled for this deployment", async () => {
    renderTab(fakeApi());
    expect(await screen.findByTestId("identity-export-not-enabled")).toHaveTextContent("Export isn't enabled for this deployment yet");
  });

  it("explains an environment that declares no identity", async () => {
    const api = fakeApi({ getStatus: vi.fn(async () => status({ hasInstance: false })) });
    renderTab(api);
    expect(await screen.findByTestId("identity-none")).toHaveTextContent("forge.ManagedIdentity");
    expect(api.listUsers).not.toHaveBeenCalled();
  });

  it("shows the reconciler's reason when the declaration was refused", async () => {
    renderTab(fakeApi({ getStatus: vi.fn(async () => status({ hasInstance: false, refused: "share_with names nobody" })) }));
    expect(await screen.findByTestId("identity-refused")).toHaveTextContent("share_with names nobody");
  });

  it("does not list users until the instance is serving", async () => {
    const api = fakeApi({ getStatus: vi.fn(async () => status({ state: "provisioning" })) });
    renderTab(api);
    expect(await screen.findByTestId("identity-users-not-serving")).toBeInTheDocument();
    expect(api.listUsers).not.toHaveBeenCalled();
  });

  it("says plainly that a local environment's IdP reports nothing here, and asks the control plane nothing", () => {
    const api = fakeApi();
    renderTab(api, { lifecycle: "local", live: null });
    expect(screen.getByTestId("identity-local")).toHaveTextContent("reports nothing to Reliant");
    expect(api.currentOrgId).not.toHaveBeenCalled();
    expect(api.getStatus).not.toHaveBeenCalled();
  });
});
