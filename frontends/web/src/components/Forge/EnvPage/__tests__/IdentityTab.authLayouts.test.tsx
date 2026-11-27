// Copyright (c) 2026 Reliant Labs

/**
 * The Auth page's two tabs (Users/AuthPage.tsx, U6d) draw the identity in the
 * "users" and "service" layouts. Each must keep what the other layouts keep:
 *
 *   - A caller the control plane refuses sees the refusal, and no list, no
 *     invite, no export: nothing of the environment is mounted.
 *   - A member sees no control they would be refused.
 *   - Switching the environment (the top bar's scope) asks for the new
 *     environment's identity and never keeps showing the previous one's.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { fakeCurrentOrgId, IdentityFakeScope, type IdentityFakes } from "@/test/identityTransport";
import type { IdentityStatus, IdentityUsersPage } from "@/services/controlPlane/identity/api";
import type { LiveEnv } from "@/services/forge/live";

import { IdentityTab, type IdentityLayout } from "../IdentityTab";

vi.mock("@/lib/toast-manager", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }));
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  currentOrgId: () => fakeCurrentOrgId(),
}));

const NOW = Date.now();

function status(issuer: string, overrides: Partial<IdentityStatus> = {}): IdentityStatus {
  return {
    hasInstance: true,
    state: "ready",
    issuer,
    domain: issuer.replace("https://", ""),
    public: false,
    adopted: false,
    apps: [{ name: "app:api", clientId: "4061000", brokerExpiresAtMs: NOW + 86_400_000 }],
    deliveries: [],
    ownerEnvironmentId: "",
    sharerEnvironmentIds: [],
    lastReconciledAtMs: NOW - 60_000,
    nextAttemptAtMs: null,
    lastError: "",
    failures: 0,
    graceUntilMs: null,
    exportUrlAvailable: false,
    refused: "",
    callerCanManage: true,
    exportEnabled: true,
    mailDelivery: "smtp",
    notEnabledReason: "unknown",
    notEnabledMessage: "",
    ownerEnvironment: null,
    sharerEnvironments: [],
    ...overrides,
  };
}

function users(userId: string, email: string): IdentityUsersPage {
  return {
    users: [{ userId, email, displayName: "", state: "active", roles: [], createdAtMs: NOW, lastSignInAtMs: null }],
    nextPageToken: "",
    totalSize: null,
    reportsSignIns: false,
    roles: [],
  };
}

const ISSUER: Record<string, string> = {
  "env-prod": "https://acme-prod.reliantidentity.com",
  "env-staging": "https://acme-staging.reliantidentity.com",
};
const USERS: Record<string, IdentityUsersPage> = {
  "env-prod": users("u-prod", "prod-person@acme.test"),
  "env-staging": users("u-staging", "staging-person@acme.test"),
};

function fakeApi(overrides: Partial<IdentityFakes> = {}): IdentityFakes {
  return {
    currentOrgId: vi.fn(async () => "org-a"),
    getStatus: vi.fn(async (_org: string, envId: string) => status(ISSUER[envId])),
    listUsers: vi.fn(async (_org: string, envId: string) => USERS[envId]),
    inviteUser: vi.fn(async () => ({ userId: "u-new", created: true, code: null })),
    setUserRoles: vi.fn(async () => null),
    sendPasswordReset: vi.fn(async () => null),
    lockUser: vi.fn(async () => {}),
    unlockUser: vi.fn(async () => {}),
    deleteUser: vi.fn(async () => {}),
    revokeSessions: vi.fn(async () => 0),
    getExportURL: vi.fn(async () => ({ url: "https://signed.example/x", expiresAtMs: NOW + 600_000 })),
    requestExport: vi.fn(async () => ({ url: "https://signed.example/y", expiresAtMs: NOW + 600_000 })),
    ...overrides,
  };
}

function tab(layout: IdentityLayout, envId: string) {
  const envName = envId.replace("env-", "");
  return (
    <IdentityTab
      envName={envName}
      lifecycle="deployed"
      live={{ id: envId, name: envName } as LiveEnv}
      liveLoading={false}
      layout={layout}
    />
  );
}

function renderLayout(api: IdentityFakes, layout: IdentityLayout, envId = "env-prod") {
  const client = createTestQueryClient();
  const view = render(
    <IdentityFakeScope fakes={api} client={client}>{tab(layout, envId)}</IdentityFakeScope>
  );
  const switchTo = (nextEnvId: string) =>
    view.rerender(
      <IdentityFakeScope fakes={api} client={client}>{tab(layout, nextEnvId)}</IdentityFakeScope>
    );
  return { ...view, switchTo };
}

const denied = () => new ConnectError("not a member of the owning organization", Code.PermissionDenied);

describe.each(["users", "service"] as const)("IdentityTab, the Auth page's %s layout", (layout) => {
  it("mounts nothing of the environment for a caller the control plane refuses", async () => {
    const api = fakeApi({
      getStatus: vi.fn(async () => {
        throw denied();
      }),
    });
    renderLayout(api, layout);

    expect(await screen.findByTestId("identity-denied")).toHaveTextContent(/can't view this environment's identity/);
    expect(screen.queryByTestId("identity-users-panel")).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-invite")).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-status")).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-export")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Export users/ })).not.toBeInTheDocument();
    expect(api.listUsers).not.toHaveBeenCalled();
  });

  it("asks for the new environment's identity when the scope changes, and shows none of the previous one's", async () => {
    const api = fakeApi();
    const view = renderLayout(api, layout, "env-prod");
    const prodMark = layout === "users" ? "prod-person@acme.test" : "acme-prod.reliantidentity.com";
    const stagingMark = layout === "users" ? "staging-person@acme.test" : "acme-staging.reliantidentity.com";
    expect((await screen.findAllByText(new RegExp(prodMark))).length).toBeGreaterThan(0);

    view.switchTo("env-staging");

    expect((await screen.findAllByText(new RegExp(stagingMark))).length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.queryByText(new RegExp(prodMark))).not.toBeInTheDocument());
    expect(api.getStatus).toHaveBeenCalledWith("org-a", "env-staging");
  });
});

describe("IdentityTab, the Auth page's tabs for a member", () => {
  it("users: the list without invite or per-person actions", async () => {
    renderLayout(fakeApi({ getStatus: vi.fn(async (_o: string, envId: string) => status(ISSUER[envId], { callerCanManage: false })) }), "users");
    expect(await screen.findByTestId("identity-read-only")).toHaveTextContent(/needs an organization admin/);
    await screen.findByTestId("identity-user-u-prod");
    expect(screen.queryByTestId("identity-invite")).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-user-actions")).not.toBeInTheDocument();
  });

  it("service: the sign-in service without an export", async () => {
    renderLayout(fakeApi({ getStatus: vi.fn(async (_o: string, envId: string) => status(ISSUER[envId], { callerCanManage: false })) }), "service");
    expect(await screen.findByTestId("identity-export-admin-only")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Export users/ })).not.toBeInTheDocument();
    expect(screen.queryByTestId("identity-users-panel")).not.toBeInTheDocument();
  });
});
