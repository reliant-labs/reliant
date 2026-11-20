import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it, vi } from "vitest";

import { FreeTierCard, FreeTierView } from "../FreeTierCard";
import type { FreeHostingStatus, MoveFreeHostingDesignationResponse } from "../hostingBillingQueries";

// The generated client lacks the pending method: the card must say "not
// available", never claim the org has no allowance.
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({}) }));

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
    sitesLimit: 3n,
    storageBytes: GiB / 4n,
    storageLimitBytes: 3n * GiB,
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
    expect(card).toHaveTextContent("2 of 3");
    expect(card).toHaveTextContent("0.25 GB of 3 GB");
    expect(card).toHaveTextContent("Up to 1 GiB per site.");
    expect(card).toHaveTextContent("2 GB of 100 GB");
    expect(card).toHaveTextContent("Resets 2026-11-30.");
    expect(screen.getAllByRole("progressbar")).toHaveLength(3);
    const sites = within(screen.getByTestId("free-tier-sites")).getAllByRole("listitem");
    expect(sites[0]).toHaveTextContent("landing");
    expect(sites[0]).toHaveTextContent("prod");
    expect(within(sites[0]).getByRole("link", { name: "https://landing.example.app" })).toHaveAttribute("href", "https://landing.example.app");
    expect(sites[0]).toHaveTextContent("0.25 GB");
    expect(sites[1]).toHaveTextContent("not measured yet");
    expect(screen.queryByTestId("free-tier-move")).not.toBeInTheDocument();
  });

  it("tells a non-founder this org has no allowance, without naming another org", () => {
    render(<FreeTierView orgName="Acme" status={status({ isDesignated: false, callerIsFounder: false, sites: [] })} onMove={vi.fn()} />);
    expect(screen.getByText("This organization doesn't have a free hosting allowance. Hosting here needs billing.")).toBeInTheDocument();
    expect(screen.queryByTestId("free-tier-move")).not.toBeInTheDocument();
  });

  it("previews the move with a dry run, then moves only on confirmation", async () => {
    const onMove = vi.fn(async (dryRun: boolean) => (dryRun ? preview() : preview({ moved: true })));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    expect(screen.getByText("Your free allowance is held by Old Co.")).toBeInTheDocument();

    fireEvent.click(screen.getByTestId("free-tier-move"));
    const confirm = await screen.findByTestId("free-tier-move-confirm");
    expect(onMove).toHaveBeenCalledExactlyOnceWith(true);
    expect(confirm).toHaveTextContent("These sites stop being served free:");
    expect(confirm).toHaveTextContent("old-site");
    expect(confirm).toHaveTextContent("marketing · prod");
    expect(screen.getByTestId("free-tier-move-cooldown")).toHaveTextContent("You can move it again after 2026-12-20.");

    fireEvent.click(within(confirm).getByRole("button", { name: "Move allowance" }));
    expect(await screen.findByTestId("free-tier-moved")).toHaveTextContent("Moved.");
    expect(onMove).toHaveBeenLastCalledWith(false);
    expect(onMove).toHaveBeenCalledTimes(2);
  });

  it("states no cooldown when none applies, and cancel sends nothing", async () => {
    const onMove = vi.fn(async () => preview({ stoppingSites: [], cooldownApplies: false, nextMoveAllowedAt: undefined }));
    render(<FreeTierView orgName="Acme" status={notHere} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    const confirm = await screen.findByTestId("free-tier-move-confirm");
    expect(confirm).toHaveTextContent("No sites stop being served free.");
    expect(screen.queryByTestId("free-tier-move-cooldown")).not.toBeInTheDocument();
    fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("free-tier-move-confirm")).not.toBeInTheDocument();
    expect(onMove).toHaveBeenCalledExactlyOnceWith(true);
  });

  it("explains the cooldown when the server refuses a move", async () => {
    const onMove = vi.fn(async () => {
      throw new ConnectError("cooldown", Code.FailedPrecondition);
    });
    render(<FreeTierView orgName="Acme" status={{ ...notHere, nextMoveAllowedAt: ts("2026-12-01T00:00:00Z") }} onMove={onMove} />);
    fireEvent.click(screen.getByTestId("free-tier-move"));
    expect(await screen.findByRole("alert")).toHaveTextContent("You moved it recently; you can move it again after 2026-12-01");
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
  it("says free tier status is not available when this build's client lacks the RPC", async () => {
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={cache}>
        <FreeTierCard orgId="org-a" orgName="Acme" />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Free tier status isn't available on this server yet.")).toBeInTheDocument();
  });
});
