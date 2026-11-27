import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { FreeTierCard, FreeTierView } from "../FreeTierCard";
import type { FreeHostingStatus, MoveFreeHostingDesignationResponse } from "../hostingBillingQueries";

afterEach(() => setTransport(null));

const ts = (iso: string) => timestampFromDate(new Date(iso));
const GiB = 2n ** 30n;

function status(overrides: Partial<FreeHostingStatus> = {}): FreeHostingStatus {
  return {
    isDesignated: true,
    heldByOrgName: "",
    callerIsFounder: true,
    sites: [
      { deploymentId: "d1", environmentName: "prod", name: "landing", url: "https://landing.example.app", storageBytes: GiB / 4n, storageMeasured: true },
      { deploymentId: "d2", environmentName: "staging", name: "docs", url: "https://docs.example.app", storageBytes: 0n, storageMeasured: false },
    ],
    sitesLimit: 5,
    storageBytes: GiB / 4n,
    storageLimitBytes: 5n * GiB,
    siteStorageLimitBytes: GiB,
    storageMeasured: true,
    transferBytes: GiB * 2n,
    transferLimitBytes: GiB * 100n,
    periodEnd: ts("2026-11-30T23:59:59Z"),
    ...overrides,
  };
}

function preview(overrides: Partial<MoveFreeHostingDesignationResponse> = {}): MoveFreeHostingDesignationResponse {
  return {
    fromOrgId: "org-old",
    toOrgId: "org-a",
    stoppingSites: [{ deploymentId: "d9", orgId: "org-old", project: "marketing", env: "prod", name: "old-site", url: "https://old.example.app" }],
    servedSites: [],
    unservedSites: [],
    cooldownApplies: true,
    nextMoveAllowedAt: ts("2026-12-20T00:00:00Z"),
    moved: false,
    ...overrides,
  };
}

const notHere = status({ isDesignated: false, heldByOrgName: "Old Co", sites: [] });

describe("FreeTierView", () => {
  it("shows the allowance this org holds: sites, storage, transfer and each site", () => {
    render(<FreeTierView orgName="Acme" status={status()} onMove={vi.fn()} />);
    const card = screen.getByTestId("free-tier-card");
    expect(card).toHaveTextContent("This organization holds the free static hosting allowance");
    expect(card).toHaveTextContent("2 of 5");
    expect(card).toHaveTextContent("0.25 GiB of 5 GiB");
    expect(card).toHaveTextContent("Up to 1 GiB per site.");
    expect(card).toHaveTextContent("2 GiB of 100 GiB");
    expect(card).toHaveTextContent("Resets 2026-11-30.");
    expect(screen.getAllByRole("progressbar")).toHaveLength(3);
    const sites = within(screen.getByTestId("free-tier-sites")).getAllByRole("listitem");
    expect(sites[0]).toHaveTextContent("landing");
    expect(sites[0]).toHaveTextContent("prod");
    expect(within(sites[0]).getByRole("link", { name: "https://landing.example.app" })).toHaveAttribute("href", "https://landing.example.app");
    expect(sites[0]).toHaveTextContent("0.25 GiB");
    expect(sites[1]).toHaveTextContent("not measured yet");
    expect(screen.queryByTestId("free-tier-move")).not.toBeInTheDocument();
  });

  it("shows a count without a Sites meter for the storage-only allowance", () => {
    render(<FreeTierView orgName="Acme" status={status({ sitesLimit: 0, storageLimitBytes: 10n * GiB, siteStorageLimitBytes: 0n })} onMove={vi.fn()} />);
    const sites = screen.getByText("Sites", { exact: true }).parentElement;
    expect(sites).toHaveTextContent(/^Sites2$/);
    expect(screen.queryByRole("progressbar", { name: /^Sites used/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("progressbar")).toHaveLength(2);
    expect(screen.getByTestId("free-tier-card")).toHaveTextContent("0.25 GiB of 10 GiB");
    expect(screen.queryByText(/per site/i)).not.toBeInTheDocument();
  });

  it("does not invent a per-site cap or meter unmeasured aggregate storage", () => {
    render(<FreeTierView orgName="Acme" status={status({ siteStorageLimitBytes: undefined, storageMeasured: false, storageBytes: 0n })} onMove={vi.fn()} />);
    expect(screen.queryByText(/per site/i)).not.toBeInTheDocument();
    expect(screen.getByTestId("free-tier-card")).toHaveTextContent("Not measured yet · 5 GiB allowance");
    expect(screen.queryByRole("progressbar", { name: /^Storage used/ })).not.toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: /^Sites used/ })).toBeInTheDocument();
  });

  it("tells a non-founder this org has no allowance, without naming another org", () => {
    render(<FreeTierView orgName="Acme" status={status({ isDesignated: false, callerIsFounder: false, sites: [] })} onMove={vi.fn()} />);
    expect(screen.getByText("This organization doesn't have a free hosting allowance. Hosting here needs billing.")).toBeInTheDocument();
    expect(screen.queryByTestId("free-tier-move")).not.toBeInTheDocument();
  });

  it("previews the move with a dry run, then moves only on confirmation", async () => {
    const onMove = vi.fn(async (dryRun: boolean) => (dryRun ? preview({ cooldownApplies: false }) : preview({ cooldownApplies: false, moved: true })));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    expect(screen.getByText("Your free allowance is held by Old Co.")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("free-tier-move"));
    const confirm = await screen.findByTestId("free-tier-move-confirm");
    expect(onMove).toHaveBeenCalledExactlyOnceWith(true);
    expect(confirm).toHaveTextContent("These sites stop being served free:");
    expect(confirm).toHaveTextContent("old-site");
    expect(confirm).toHaveTextContent("marketing · prod");
    expect(screen.getByTestId("free-tier-move-cooldown-note")).toBeInTheDocument();

    fireEvent.click(within(confirm).getByRole("button", { name: "Move allowance" }));
    expect(await screen.findByTestId("free-tier-moved")).toHaveTextContent("Moved.");
    expect(onMove).toHaveBeenLastCalledWith(false);
    expect(onMove).toHaveBeenCalledTimes(2);
  });

  it("starts the move over when the org changes during confirmation: A's preview never confirms a move to B", async () => {
    const moveA = vi.fn(async () => preview({ cooldownApplies: false }));
    const moveB = vi.fn(async () => preview({ toOrgId: "org-b", cooldownApplies: false }));
    const { rerender } = render(<FreeTierView orgId="org-a" orgName="Acme" status={notHere} onMove={moveA} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    await screen.findByTestId("free-tier-move-confirm");

    rerender(<FreeTierView orgId="org-b" orgName="Beta" status={notHere} onMove={moveB} />);
    expect(screen.queryByTestId("free-tier-move-confirm")).not.toBeInTheDocument();
    expect(screen.getByTestId("free-tier-move")).toBeInTheDocument();
    expect(moveB).not.toHaveBeenCalled();
    expect(moveA).toHaveBeenCalledExactlyOnceWith(true);
  });

  it("drops a preview still in flight when the org changes", async () => {
    let resolveA!: (r: MoveFreeHostingDesignationResponse) => void;
    const moveA = vi.fn(() => new Promise<MoveFreeHostingDesignationResponse>((r) => { resolveA = r; }));
    const moveB = vi.fn(async () => preview({ toOrgId: "org-b", cooldownApplies: false }));
    const { rerender } = render(<FreeTierView orgId="org-a" orgName="Acme" status={notHere} onMove={moveA} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    rerender(<FreeTierView orgId="org-b" orgName="Beta" status={notHere} onMove={moveB} />);
    await act(async () => resolveA(preview({ cooldownApplies: false })));
    expect(screen.queryByTestId("free-tier-move-confirm")).not.toBeInTheDocument();
    expect(screen.getByTestId("free-tier-move")).toBeInTheDocument();
    expect(moveB).not.toHaveBeenCalled();
  });

  it("blocks the move on cooldown_applies, and cancel sends nothing", async () => {
    const onMove = vi.fn(async () => preview({ cooldownApplies: true }));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    const confirm = await screen.findByTestId("free-tier-move-confirm");
    expect(screen.getByTestId("free-tier-move-cooldown")).toHaveTextContent("You can move it after 2026-12-20.");
    expect(within(confirm).getByRole("button", { name: "Move allowance" })).toBeDisabled();
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("free-tier-move-confirm")).not.toBeInTheDocument();
    expect(onMove).toHaveBeenCalledExactlyOnceWith(true);
  });

  it("never treats next_move_allowed_at alone as blocked: an exempt move stays allowed", async () => {
    const onMove = vi.fn(async () => preview({ cooldownApplies: false, nextMoveAllowedAt: ts("2026-12-20T00:00:00Z") }));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    const confirm = await screen.findByTestId("free-tier-move-confirm");
    expect(screen.queryByTestId("free-tier-move-cooldown")).not.toBeInTheDocument();
    expect(within(confirm).getByRole("button", { name: "Move allowance" })).toBeEnabled();
  });

  it("says the org already holds it when the dry run names the same org", async () => {
    const onMove = vi.fn(async () => preview({ fromOrgId: "org-a", toOrgId: "org-a", stoppingSites: [], cooldownApplies: false }));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    expect(await screen.findByTestId("free-tier-already")).toHaveTextContent("Acme already holds your free static hosting allowance");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("treats moved=false on a real move as already designated, not a failure", async () => {
    const onMove = vi.fn(async () => preview({ cooldownApplies: false, moved: false }));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    fireEvent.click(within(await screen.findByTestId("free-tier-move-confirm")).getByRole("button", { name: "Move allowance" }));
    expect(await screen.findByTestId("free-tier-already")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it.each([
    ["not_org_member", Code.PermissionDenied, "permission denied", "You're not a member of Acme"],
    ["free_hosting_founder_only", Code.PermissionDenied, "permission denied", "Only the founder who owns the free allowance can move it"],
    ["free_hosting_target_billed", Code.FailedPrecondition, "target billed", "Acme already pays for app hosting"],
    ["free_hosting_move_cooldown", Code.FailedPrecondition, "free hosting moved recently; next move allowed at 2026-12-01T00:00:00Z", "You can move it again after 2026-12-01"],
  ])("explains the %s refusal", async (reason, code, message, expected) => {
    const onMove = vi.fn(async (dryRun: boolean) => {
      if (dryRun) return preview({ cooldownApplies: false });
      throw new ConnectError(message, code, new Headers({ "x-forge-error-reason": reason }));
    });
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    fireEvent.click(within(await screen.findByTestId("free-tier-move-confirm")).getByRole("button", { name: "Move allowance" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
  });

  it("says moving isn't available when the RPC is unserved", async () => {
    const onMove = vi.fn(async () => {
      throw new ConnectError("no", Code.Unimplemented);
    });
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    expect(await screen.findByRole("alert")).toHaveTextContent("Moving the allowance isn't available yet.");
  });

  it("shows a loading line", () => {
    render(<FreeTierView orgName="Acme" loading onMove={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading free tier status…");
  });

  it("treats an unserved status RPC as unavailable", () => {
    render(<FreeTierView orgName="Acme" error={new ConnectError("no", Code.Unimplemented)} onMove={vi.fn()} />);
    expect(screen.getByText("Free tier status isn't available on this server yet.")).toBeInTheDocument();
    expect(screen.queryByText(/needs billing/)).not.toBeInTheDocument();
  });

  it("keeps a failed status read retryable", () => {
    const onRetry = vi.fn();
    render(<FreeTierView orgName="Acme" error={new ConnectError("down", Code.Unavailable)} onRetry={onRetry} onMove={vi.fn()} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Could not load free tier status.");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});

describe("FreeTierCard", () => {
  it("says free tier status is not available when the server lacks the RPC", async () => {
    setTransport(mockTransport());
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={cache}>
        <FreeTierCard orgId="org-a" orgName="Acme" />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Free tier status isn't available on this server yet.")).toBeInTheDocument();
  });
});
