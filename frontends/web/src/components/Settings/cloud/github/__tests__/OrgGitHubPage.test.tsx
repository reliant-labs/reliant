// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Organization GitHub: an admin connects their own GitHub, links
 * an installation the App already has (Start's state, then Link), installs
 * the App anew, finishes a link GitHub's setup redirect brought back, and
 * unlinks; a member only reads the list. Every refusal is said in words.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import type { MyOrg } from "@/services/controlPlane/orgs";

const mocks = vi.hoisted(() => ({
  org: null as MyOrg | null,
  hasToken: true,
  search: {} as Record<string, string | undefined>,
  navigate: vi.fn(),
  connect: vi.fn(),
  start: vi.fn(),
  link: vi.fn(),
  unlink: vi.fn(),
  linkable: vi.fn(),
  assign: vi.fn(),
}));

vi.mock("@/services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({ active: mocks.org, orgs: mocks.org ? [mocks.org] : [], isLoading: false }),
}));
vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => ({
    hasToken: mocks.hasToken,
    accountLogin: mocks.hasToken ? "alex-demo" : undefined,
    installations: [],
    scopes: "",
    isLoading: false,
    isError: false,
    refresh: vi.fn(),
  }),
}));
vi.mock("@/lib/github-connect", () => ({ connectGitHub: () => mocks.connect() }));
vi.mock("@/lib/constants", async (orig) => ({ ...(await orig<object>()), isElectron: () => false }));
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => mocks.search,
  useNavigate: () => mocks.navigate,
}));

import { OrgGitHubPage } from "../OrgGitHubPage";

const acme = (role: string): MyOrg => ({ id: "org-a", name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true });

const reason = (code: Code, r: string) => new ConnectError(r, code, { "x-forge-error-reason": r });

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <OrgGitHubPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [mocks.navigate, mocks.connect, mocks.start, mocks.link, mocks.unlink, mocks.linkable, mocks.assign]) fn.mockReset();
  mocks.org = acme("admin");
  mocks.hasToken = true;
  mocks.search = {};
  mocks.connect.mockResolvedValue({ kind: "redirecting" });
  mocks.start.mockResolvedValue({ installUrl: "https://github.com/apps/reliant-labs/installations/new?state=s1", state: "s1" });
  mocks.link.mockImplementation((req: { installationId: bigint }) => ({
    installation: { installationId: req.installationId, orgId: "org-a", accountLogin: "alex-demo", accountType: "User" },
  }));
  mocks.unlink.mockResolvedValue({});
  mocks.linkable.mockResolvedValue({
    installations: [
      { installationId: 7n, accountLogin: "acme", accountType: "Organization", repositorySelection: "all", linkedHere: true },
      { installationId: 8n, accountLogin: "alex-demo", accountType: "User", repositorySelection: "selected" },
      { installationId: 9n, accountLogin: "other-co", accountType: "Organization", linkedElsewhere: true },
    ],
  });
  Object.defineProperty(window, "location", { configurable: true, value: { ...window.location, assign: mocks.assign } });
  setTransport(
    mockTransport({
      "OrganizationService.ListGitHubAppInstallations": () => ({
        installations: [{ installationId: 7n, orgId: "org-a", accountLogin: "acme", accountType: "Organization", linkedAt: { seconds: 1_700_000_000n, nanos: 0 } }],
      }),
      "OrganizationService.ListLinkableGitHubAppInstallations": () => mocks.linkable(),
      "OrganizationService.StartGitHubAppInstall": (req) => mocks.start(req),
      "OrganizationService.LinkGitHubAppInstallation": (req) => mocks.link(req),
      "OrganizationService.UnlinkGitHubAppInstallation": (req) => mocks.unlink(req),
    }),
  );
});

describe("OrgGitHubPage", () => {
  it("lists the linked installations, and marks where each linkable one is linked", async () => {
    renderPage();
    const linked = await screen.findAllByTestId("org-github-linked-row");
    expect(linked).toHaveLength(1);
    expect(within(linked[0]).getByText("acme")).toBeInTheDocument();
    expect(within(linked[0]).getByText(/GitHub organization · Linked/)).toBeInTheDocument();

    const rows = await screen.findAllByTestId("org-github-linkable-row");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByTestId("org-github-linkable-state")).toHaveTextContent("Linked here");
    expect(within(rows[1]).getByRole("button", { name: "Link alex-demo" })).toBeInTheDocument();
    expect(within(rows[2]).getByTestId("org-github-linkable-state")).toHaveTextContent("Linked to another organization");
  });

  it("links an installation the App already has with a fresh state from Start", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Link alex-demo" }));
    await waitFor(() => expect(mocks.link).toHaveBeenCalledTimes(1));
    expect(mocks.start.mock.calls[0][0]).toMatchObject({ orgId: "org-a" });
    expect(mocks.link.mock.calls[0][0]).toMatchObject({ installationId: 8n, state: "s1" });
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent("@alex-demo is linked to Acme.");
  });

  it("installs the App by sending the browser to Start's URL", async () => {
    renderPage();
    await screen.findAllByTestId("org-github-linkable-row");
    fireEvent.click(screen.getByTestId("org-github-install"));
    await waitFor(() => expect(mocks.assign).toHaveBeenCalledWith("https://github.com/apps/reliant-labs/installations/new?state=s1"));
  });

  it("finishes the link GitHub's setup redirect brought back, once, and clears it from the URL", async () => {
    mocks.search = { installation_id: "8", link_state: "s-from-github" };
    renderPage();
    await waitFor(() => expect(mocks.link).toHaveBeenCalledTimes(1));
    expect(mocks.link.mock.calls[0][0]).toMatchObject({ installationId: 8n, state: "s-from-github" });
    expect(mocks.navigate).toHaveBeenCalledWith(expect.objectContaining({ replace: true }));
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent("is linked to Acme");
  });

  it("says when the setup redirect named no installation", async () => {
    mocks.search = { link_state: "s-from-github" };
    renderPage();
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent("GitHub didn't name an installation");
    expect(mocks.link).not.toHaveBeenCalled();
  });

  it.each([
    [reason(Code.InvalidArgument, "github_install_state_invalid"), "This link expired or was started by someone else"],
    [reason(Code.FailedPrecondition, "github_installation_not_visible"), "Your GitHub account can't see that installation"],
    [reason(Code.FailedPrecondition, "github_installation_linked_elsewhere"), "linked to another Reliant organization"],
    [reason(Code.PermissionDenied, "admin_required"), "Only an owner or admin of Acme"],
    [reason(Code.FailedPrecondition, "git_credential_not_connected"), "Connect your GitHub account first"],
  ])("names a refused link in words (%s)", async (err, text) => {
    mocks.link.mockRejectedValue(err);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Link alex-demo" }));
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent(text);
  });

  it("asks an admin without a GitHub connection to connect first, and lists nothing", async () => {
    mocks.hasToken = false;
    renderPage();
    fireEvent.click(await screen.findByTestId("org-github-connect"));
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(1));
    expect(mocks.linkable).not.toHaveBeenCalled();
  });

  it("offers Reconnect when GitHub rejected the saved sign-in", async () => {
    mocks.linkable.mockRejectedValue(reason(Code.FailedPrecondition, "git_credential_needs_reconnect"));
    renderPage();
    expect(await screen.findByTestId("org-github-connect")).toHaveTextContent("Reconnect GitHub");
  });

  it("unlinks after a confirmation", async () => {
    renderPage();
    fireEvent.click(await screen.findByTestId("org-github-unlink"));
    fireEvent.click(screen.getByRole("button", { name: "Unlink" }));
    await waitFor(() => expect(mocks.unlink).toHaveBeenCalledTimes(1));
    expect(mocks.unlink.mock.calls[0][0]).toMatchObject({ orgId: "org-a", installationId: 7n });
    expect(await screen.findByTestId("org-github-notice")).toHaveTextContent("@acme is unlinked from Acme.");
  });

  it("a member reads the list, with no actions and no GitHub reads of their own", async () => {
    mocks.org = acme("member");
    renderPage();
    await screen.findAllByTestId("org-github-linked-row");
    expect(screen.queryByTestId("org-github-unlink")).toBeNull();
    expect(screen.queryByTestId("org-github-link")).toBeNull();
    expect(screen.getByTestId("org-github-member-note")).toHaveTextContent("Only owners and admins of Acme");
    expect(mocks.linkable).not.toHaveBeenCalled();
  });
});
