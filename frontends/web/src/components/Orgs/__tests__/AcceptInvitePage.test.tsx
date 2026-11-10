/**
 * /accept-invite, over the real generated client on a mock transport.
 *
 * Pinned: the page works from the token the boot capture stored, never the
 * URL; a signed-out (or anonymous) visitor is sent through sign-in and back
 * here with the invitation kept; a wrong account is told so BEFORE any accept
 * button; accepting joins, switches the app to the joined org and forgets the
 * token; an invitation that can never be used says why and is forgotten.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render as rtlRender, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", async (orig) => ({
  ...(await orig<typeof import("@tanstack/react-router")>()),
  useNavigate: () => navigate,
}));
const hardNavigate = vi.fn();
vi.mock("@/lib/hardNavigate", () => ({ hardNavigate: (p: string) => hardNavigate(p) }));
const logWarn = vi.fn();
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn(), warn: (...a: unknown[]) => logWarn(...a), error: vi.fn(), debug: vi.fn() } }));

import { setTransport } from "@/lib/connect";
import { captureInviteLinkFromUrl, readPendingInvite, resetInviteLinkMemoryForTests } from "@/lib/inviteLink";
import { mockTransport } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

import { AcceptInvitePage } from "../AcceptInvitePage";

const TOKEN = "rinv_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_";

const PREVIEW = {
  orgName: "Acme",
  role: "admin",
  invitedByName: "Ada",
  inviteeEmailHint: "b***@example.com",
  status: "pending",
  expiresAt: timestampFromDate(new Date(Date.now() + 3 * 86_400_000)),
  emailMatches: true,
  alreadyMember: false,
};

const refusal = (reason: string, code = Code.FailedPrecondition) =>
  new ConnectError("prose", code, { "x-forge-error-reason": reason });

type Handlers = Parameters<typeof mockTransport>[0];

function withLink() {
  window.history.replaceState(null, "", `/accept-invite#${TOKEN}`);
  captureInviteLinkFromUrl();
}

function render(handlers: Handlers = {}) {
  const accept = vi.fn((_req: unknown) => ({ organization: { id: "org-acme", name: "Acme", slug: "acme", role: "admin" }, userId: "u_bob" }));
  const preview = vi.fn((_req: unknown) => PREVIEW);
  setTransport(
    mockTransport({
      "OrganizationService.GetInvitation": preview,
      "OrganizationService.AcceptInvitation": accept,
      "OrganizationService.ListMyOrganizations": { response: { organizations: [], defaultOrgId: "" } },
      ...handlers,
    }),
  );
  renderWithQuery(<AcceptInvitePage />);
  return { accept, preview };
}

const signIn = (user: Record<string, unknown> | null) => {
  const signOut = vi.fn(async () => useAuthStore.setState({ user: null }));
  useAuthStore.setState({ user: user as never, initialized: true, signOut } as never);
  return signOut;
};

beforeEach(() => {
  localStorage.clear();
  resetInviteLinkMemoryForTests();
  navigate.mockReset();
  hardNavigate.mockReset();
  logWarn.mockReset();
  setTransport(null);
  useActiveOrgStore.setState({ userId: "u_bob", chosenId: null, effectiveId: null });
  window.history.replaceState(null, "", "/");
});

describe("AcceptInvitePage", () => {
  it("without a link, says to open the one from the email", async () => {
    signIn({ id: "u_bob", email: "bob@example.com" });
    const { preview } = render();
    expect(await screen.findByText("Open the invitation link from your email")).toBeInTheDocument();
    expect(preview).not.toHaveBeenCalled();
  });

  it("signed out: sends the visitor through sign-in and back here, keeping the invitation", async () => {
    withLink();
    signIn(null);
    const { preview } = render();
    await userEvent.click(await screen.findByTestId("accept-invite-sign-in"));
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith("/auth?redirect=%2Faccept-invite"));
    expect(preview).not.toHaveBeenCalled();
    expect(readPendingInvite()?.token).toBe(TOKEN);
  });

  it("an anonymous session is signed out first: it can never accept", async () => {
    withLink();
    const signOut = signIn({ id: "anon", is_anonymous: true });
    render();
    await userEvent.click(await screen.findByTestId("accept-invite-sign-in"));
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith("/auth?redirect=%2Faccept-invite"));
    expect(signOut).toHaveBeenCalled();
  });

  it("previews, joins, switches the app to the joined org, and forgets the token", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    const { accept, preview } = render();
    expect(await screen.findByTestId("accept-invite-org")).toHaveTextContent("Acme");
    expect(screen.getByTestId("accept-invite-card")).toHaveTextContent("Role: Admin · invited by Ada");
    expect(preview).toHaveBeenCalledWith(expect.objectContaining({ token: TOKEN }));

    await userEvent.click(screen.getByTestId("accept-invite-submit"));
    await waitFor(() => expect(accept).toHaveBeenCalledWith(expect.objectContaining({ token: TOKEN })));
    expect(await screen.findByTestId("accept-invite-joined")).toHaveTextContent("You're in Acme");
    expect(useActiveOrgStore.getState().chosenId).toBe("org-acme");
    expect(readPendingInvite()).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Open Acme" }));
    expect(navigate).toHaveBeenCalledWith({ to: "/" });
  });

  it("a second invitation in the same app never shows the first one's preview", async () => {
    const tokenOf = (input: unknown) => (input as { token?: string }).token;
    const TOKEN_B = "rinv_BbBbQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_";
    signIn({ id: "u_bob", email: "bob@example.com" });
    const accept = vi.fn((input: unknown) => ({
      organization: tokenOf(input) === TOKEN_B ? { id: "org-beta", name: "Beta", slug: "beta", role: "member" } : { id: "org-acme", name: "Acme", slug: "acme", role: "admin" },
      userId: "u_bob",
    }));
    const preview = vi.fn((input: unknown) => (tokenOf(input) === TOKEN_B ? { ...PREVIEW, orgName: "Beta", role: "member" } : PREVIEW));
    setTransport(
      mockTransport({
        "OrganizationService.GetInvitation": preview,
        "OrganizationService.AcceptInvitation": accept,
        "OrganizationService.ListMyOrganizations": { response: { organizations: [], defaultOrgId: "" } },
      }),
    );
    // One client for both visits, keeping its cache the way the app's does.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const page = () => (
      <QueryClientProvider client={client}>
        <AcceptInvitePage />
      </QueryClientProvider>
    );

    withLink();
    const first = rtlRender(page());
    expect(await screen.findByTestId("accept-invite-org")).toHaveTextContent("Acme");
    first.unmount();

    // Another tab stores invitation B; this tab comes back to /accept-invite.
    window.history.replaceState(null, "", `/accept-invite#${TOKEN_B}`);
    captureInviteLinkFromUrl();
    rtlRender(page());
    expect(await screen.findByTestId("accept-invite-org")).toHaveTextContent("Beta");
    expect(preview).toHaveBeenLastCalledWith(expect.objectContaining({ token: TOKEN_B }));

    await userEvent.click(screen.getByTestId("accept-invite-submit"));
    await waitFor(() => expect(accept).toHaveBeenCalledWith(expect.objectContaining({ token: TOKEN_B })));
    expect(await screen.findByTestId("accept-invite-joined")).toHaveTextContent("You're in Beta");
  });

  it("a different account is told so before there is anything to press", async () => {
    withLink();
    signIn({ id: "u_eve", email: "eve@example.com" });
    const { accept } = render({ "OrganizationService.GetInvitation": { response: { ...PREVIEW, emailMatches: false } } });
    const notice = await screen.findByTestId("accept-invite-mismatch");
    expect(notice).toHaveTextContent("b***@example.com");
    expect(notice).toHaveTextContent("eve@example.com");
    expect(screen.queryByTestId("accept-invite-submit")).toBeNull();
    expect(accept).not.toHaveBeenCalled();
    // The invitation stays for the right person.
    expect(readPendingInvite()?.token).toBe(TOKEN);
  });

  it("a mismatch the server finds at accept shows the same notice and keeps the invitation", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.AcceptInvitation": { error: refusal("invite_email_mismatch", Code.PermissionDenied) } });
    await userEvent.click(await screen.findByTestId("accept-invite-submit"));
    expect(await screen.findByTestId("accept-invite-mismatch")).toBeInTheDocument();
    expect(readPendingInvite()?.token).toBe(TOKEN);
  });

  it.each([
    ["expired", "This invitation has expired"],
    ["revoked", "This invitation was withdrawn"],
    ["accepted", "This invitation has already been used"],
  ])("a %s invitation says so and is forgotten", async (status, title) => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.GetInvitation": { response: { ...PREVIEW, status } } });
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.queryByTestId("accept-invite-submit")).toBeNull();
    await waitFor(() => expect(readPendingInvite()).toBeNull());
  });

  it("an unknown token is not valid, with one answer for unknown and wrong", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.GetInvitation": { error: new ConnectError("not found", Code.NotFound) } });
    expect(await screen.findByTestId("accept-invite-invalid")).toBeInTheDocument();
  });

  it("an existing member has nothing to accept", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.GetInvitation": { response: { ...PREVIEW, alreadyMember: true } } });
    expect(await screen.findByTestId("accept-invite-already-member")).toHaveTextContent("You're already in Acme");
  });

  it("a full org says so in the invitee's words, and lets them try again", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.AcceptInvitation": { error: refusal("seat_limit_reached", Code.ResourceExhausted) } });
    await userEvent.click(await screen.findByTestId("accept-invite-submit"));
    expect(await screen.findByTestId("accept-invite-error")).toHaveTextContent(/no free seats/);
    expect(screen.getByTestId("accept-invite-submit")).toBeEnabled();
    expect(readPendingInvite()?.token).toBe(TOKEN);
  });

  it("an unverified email is asked to verify first", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.GetInvitation": { error: refusal("email_unverified", Code.PermissionDenied) } });
    expect(await screen.findByText("Verify your email first")).toBeInTheDocument();
  });

  it("never logs the token", async () => {
    withLink();
    signIn({ id: "u_bob", email: "bob@example.com" });
    render({ "OrganizationService.AcceptInvitation": { error: new ConnectError("boom", Code.Internal) } });
    await userEvent.click(await screen.findByTestId("accept-invite-submit"));
    await screen.findByTestId("accept-invite-error");
    expect(logWarn).toHaveBeenCalled();
    expect(JSON.stringify(logWarn.mock.calls)).not.toContain(TOKEN);
  });
});
