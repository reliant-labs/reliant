// Copyright (c) 2025 Reliant Labs

/**
 * The Identity tab tells the truth about email, and hands out password links.
 *
 *   - MAIL NONE. The environment declares no smtp, so nothing is ever emailed.
 *     The tab says so with the fix, offers no "email" verb (the server would
 *     refuse it), and invite / reset hand back a one-time link instead.
 *   - THE LINK IS A CREDENTIAL. Shown once in a dialog with copy, never in a
 *     toast, gone when the dialog closes.
 *   - identity:manage. Returning a code needs that permission on top of the
 *     admin role; an admin without it is told so in context.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { IdentityApiProvider, type IdentityApi } from "@/services/controlPlane/identity/IdentityApiContext";
import type { IdentityPasswordCode, IdentityStatus, IdentityUsersPage } from "@/services/controlPlane/identity/api";
import type { LiveEnv } from "@/services/forge/live";

import { IdentityTab } from "../IdentityTab";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

const NOW = Date.now();
const LIVE = { id: "env-staging", name: "staging" } as LiveEnv;
const CODE = "Q7ZK2M";
const LINK = `https://eager-sandpiper.sites.reliantlabs.dev/auth/reset?userID=u-alice&code=${CODE}&orgID=o1`;

function status(overrides: Partial<IdentityStatus> = {}): IdentityStatus {
  return {
    hasInstance: true,
    state: "ready",
    issuer: "https://mi-dogfood-staging-65227a.reliantidentity.com",
    domain: "mi-dogfood-staging-65227a.reliantidentity.com",
    public: false,
    adopted: false,
    apps: [],
    deliveries: [],
    ownerEnvironmentId: "env-staging",
    sharerEnvironmentIds: [],
    lastReconciledAtMs: NOW - 60_000,
    nextAttemptAtMs: null,
    lastError: "",
    failures: 0,
    graceUntilMs: null,
    exportUrlAvailable: false,
    refused: "",
    callerCanManage: true,
    exportEnabled: false,
    mailDelivery: "none",
    ...overrides,
  };
}

const USERS: IdentityUsersPage = {
  users: [{ userId: "u-alice", email: "alice@acme.test", displayName: "", state: "active", roles: [], createdAtMs: NOW }],
  nextPageToken: "",
  roles: [{ key: "writer", displayName: "Writer", group: "", declared: false }],
};

function code(overrides: Partial<IdentityPasswordCode> = {}): IdentityPasswordCode {
  // Two hours from the moment the fake answers, so "in 2h" holds however long
  // the render takes (an hour exactly would read "60m" a millisecond later).
  return { code: CODE, expiresAtMs: Date.now() + 2 * 3_600_000, resetUrl: LINK, ...overrides };
}

function fakeApi(overrides: Partial<IdentityApi> = {}): IdentityApi {
  return {
    currentOrgId: vi.fn(async () => "org-a"),
    getStatus: vi.fn(async () => status()),
    listUsers: vi.fn(async () => USERS),
    inviteUser: vi.fn(async () => ({ userId: "u-new", created: true, code: code() })),
    setUserRoles: vi.fn(async () => null),
    sendPasswordReset: vi.fn(async () => code()),
    lockUser: vi.fn(async () => {}),
    unlockUser: vi.fn(async () => {}),
    deleteUser: vi.fn(async () => {}),
    revokeSessions: vi.fn(async () => 0),
    getExportURL: vi.fn(async () => ({ url: "", expiresAtMs: null })),
    ...overrides,
  };
}

function renderTab(api: IdentityApi) {
  return render(
    <QueryClientProvider client={createTestQueryClient()}>
      <IdentityApiProvider api={api}>
        <IdentityTab envName="staging" lifecycle="deployed" live={LIVE} liveLoading={false} envNameById={() => undefined} />
      </IdentityApiProvider>
    </QueryClientProvider>
  );
}

/** No toast, ever, may carry the code. */
function expectNoToastLeaks() {
  for (const fn of Object.values(toasts)) {
    for (const call of fn.mock.calls) expect(JSON.stringify(call)).not.toContain(CODE);
  }
}

beforeEach(() => {
  for (const fn of Object.values(toasts)) fn.mockReset();
});

describe("IdentityTab with no mail delivery", () => {
  it("says plainly that no email is sent, and how to declare smtp", async () => {
    renderTab(fakeApi());
    const mail = await screen.findByTestId("identity-mail");
    expect(mail).toHaveTextContent("Reliant sends no email for this environment");
    expect(screen.getByTestId("identity-mail-none-hint")).toHaveTextContent("smtp = forge.IdentitySMTP");
    expect(screen.getByTestId("identity-mail-none-hint")).toHaveTextContent("forge.ManagedIdentity");
  });

  it("offers no email verb, only the link", async () => {
    renderTab(fakeApi());
    const alice = await screen.findByTestId("identity-user-u-alice");
    expect(within(alice).queryByRole("button", { name: "Email reset" })).not.toBeInTheDocument();
    expect(within(alice).getByRole("button", { name: "Get reset link" })).toBeInTheDocument();

    const invite = screen.getByTestId("identity-invite");
    expect(invite).toHaveTextContent("sends no email, so nothing is emailed");
    expect(within(invite).queryByTestId("identity-invite-want-link")).not.toBeInTheDocument();
    expect(within(invite).getByRole("button", { name: "Invite and get link" })).toBeInTheDocument();
  });

  it("shows a reset link once, copies it, and forgets it on close", async () => {
    const user = userEvent.setup();
    const api = fakeApi();
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");

    await user.click(within(alice).getByRole("button", { name: "Get reset link" }));
    await waitFor(() => expect(api.sendPasswordReset).toHaveBeenCalledWith("org-a", "env-staging", "u-alice", { returnCode: true }));

    const dialog = await screen.findByRole("dialog", { name: "Password reset link for alice@acme.test" });
    expect(within(dialog).getByTestId("identity-code-link")).toHaveValue(LINK);
    expect(within(dialog).getByTestId("identity-code-dialog")).toHaveTextContent("expires in 2h");

    await user.click(within(dialog).getByRole("button", { name: "Copy" }));
    expect(await navigator.clipboard.readText()).toBe(LINK);
    expect(within(dialog).getByRole("button", { name: "Copied" })).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(CODE);
    expect(document.body.innerHTML).not.toContain(CODE);
    expectNoToastLeaks();
  });

  it("falls back to the bare code when the environment has no sign-in address", async () => {
    const api = fakeApi({ sendPasswordReset: vi.fn(async () => code({ resetUrl: "" })) });
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");
    await userEvent.click(within(alice).getByRole("button", { name: "Get reset link" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("identity-code-value")).toHaveValue(CODE);
    expect(within(dialog).getByTestId("identity-code-no-link")).toHaveTextContent("/auth/reset");
  });

  it("invites by handing back a sign-up link", async () => {
    const api = fakeApi();
    renderTab(api);
    const invite = await screen.findByTestId("identity-invite");
    await screen.findByTestId("identity-user-u-alice");
    await userEvent.type(within(invite).getByTestId("identity-invite-email"), "carol@acme.test");
    await userEvent.click(within(invite).getByRole("button", { name: "Invite and get link" }));

    await waitFor(() =>
      expect(api.inviteUser).toHaveBeenCalledWith("org-a", "env-staging", { email: "carol@acme.test", roles: [], returnCode: true })
    );
    const dialog = await screen.findByRole("dialog", { name: "Sign-up link for carol@acme.test" });
    expect(within(dialog).getByTestId("identity-code-link")).toHaveValue(LINK);
    expect(within(invite).getByTestId("identity-invite-email")).toHaveValue("");
    expectNoToastLeaks();
  });

  it("says an existing account with a password got the roles, not a link", async () => {
    const api = fakeApi({ inviteUser: vi.fn(async () => ({ userId: "u-alice", created: false, code: null })) });
    renderTab(api);
    const invite = await screen.findByTestId("identity-invite");
    await screen.findByTestId("identity-user-u-alice");
    await userEvent.type(within(invite).getByTestId("identity-invite-email"), "alice@acme.test");
    await userEvent.click(within(invite).getByRole("button", { name: "Invite and get link" }));

    await waitFor(() => expect(toasts.success).toHaveBeenCalledWith(expect.stringContaining("already had an account and a password")));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("explains a missing identity:manage permission in context, not as a toast", async () => {
    const refused = new ConnectError(
      "returning a password-set code needs the identity:manage permission, which you do not hold; ask an organization owner to grant it",
      Code.PermissionDenied
    );
    const api = fakeApi({
      sendPasswordReset: vi.fn(async () => {
        throw refused;
      }),
    });
    renderTab(api);
    const alice = await screen.findByTestId("identity-user-u-alice");
    await userEvent.click(within(alice).getByRole("button", { name: "Get reset link" }));

    const dialog = await screen.findByRole("dialog", { name: "You can't hand out password links here" });
    expect(within(dialog).getByTestId("identity-code-refused")).toHaveTextContent("identity:manage");
    expect(within(dialog).getByTestId("identity-code-refused")).toHaveTextContent("ask an organization owner to grant it");
    for (const fn of Object.values(toasts)) expect(fn).not.toHaveBeenCalled();

    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("explains a refused invite link the same way", async () => {
    const api = fakeApi({
      inviteUser: vi.fn(async () => {
        throw new ConnectError("needs identity:manage", Code.PermissionDenied);
      }),
    });
    renderTab(api);
    const invite = await screen.findByTestId("identity-invite");
    await screen.findByTestId("identity-user-u-alice");
    await userEvent.type(within(invite).getByTestId("identity-invite-email"), "carol@acme.test");
    await userEvent.click(within(invite).getByRole("button", { name: "Invite and get link" }));

    expect(await screen.findByRole("dialog", { name: "You can't hand out password links here" })).toBeInTheDocument();
    expect(within(invite).queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("IdentityTab with smtp", () => {
  it("says mail goes through the environment's SMTP server, and offers both email and the link", async () => {
    const api = fakeApi({ getStatus: vi.fn(async () => status({ mailDelivery: "smtp" })) });
    renderTab(api);
    expect(await screen.findByTestId("identity-mail")).toHaveTextContent("own SMTP server");
    const alice = await screen.findByTestId("identity-user-u-alice");
    expect(within(alice).getByRole("button", { name: "Email reset" })).toBeInTheDocument();
    expect(within(alice).getByRole("button", { name: "Get reset link" })).toBeInTheDocument();
  });

  it("emails an invitation by default, and hands back the link when asked", async () => {
    const api = fakeApi({ getStatus: vi.fn(async () => status({ mailDelivery: "smtp" })) });
    renderTab(api);
    const invite = await screen.findByTestId("identity-invite");
    await screen.findByTestId("identity-user-u-alice");

    await userEvent.click(within(invite).getByTestId("identity-invite-want-link"));
    await userEvent.type(within(invite).getByTestId("identity-invite-email"), "dan@acme.test");
    await userEvent.click(within(invite).getByRole("button", { name: "Invite and get link" }));
    await waitFor(() =>
      expect(api.inviteUser).toHaveBeenCalledWith("org-a", "env-staging", { email: "dan@acme.test", roles: [], returnCode: true })
    );
    expect(await screen.findByRole("dialog", { name: "Sign-up link for dan@acme.test" })).toBeInTheDocument();
  });
});

describe("IdentityTab against a server that predates mail_delivery", () => {
  it("claims neither, and keeps offering email", async () => {
    renderTab(fakeApi({ getStatus: vi.fn(async () => status({ mailDelivery: "unknown" })) }));
    const alice = await screen.findByTestId("identity-user-u-alice");
    expect(screen.queryByTestId("identity-mail")).not.toBeInTheDocument();
    expect(within(alice).getByRole("button", { name: "Email reset" })).toBeInTheDocument();
  });
});
