// Copyright (c) 2026 Reliant Labs

/**
 * The ML gate (MlRoute MlGatedView) over an in-memory MlApi:
 *
 *   - an org without `ml` gets the one access page, and its view event;
 *   - Request access records the request and says "Requested on …";
 *   - an org that already asked sees that, with the button disabled;
 *   - a failed request offers the support email;
 *   - an entitled org whose ML read answers FEATURE_NOT_ENTITLED (a
 *     revocation since) gets the access page, not an error;
 *   - that refusal is the org's alone, and lifts when the entitlement
 *     changes (granted again) or a request answers "already enabled";
 *   - a request made for one org never shows on another org's access page;
 *   - an entitled org on a control plane without the data gets the calm
 *     empty state, never invented rows.
 */

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { mlKeys } from "@/hooks/ml-queries";
import { MlApiProvider, type MlApi } from "@/services/controlPlane/ml/MlApiContext";
import type { MlOutcome, OrgEntitlement } from "@/services/controlPlane/ml/types";

const tracked = vi.hoisted(() => [] as { name: string; props?: Record<string, unknown> }[]);
vi.mock("@/lib/analytics", () => ({ trackEvent: (name: string, props?: Record<string, unknown>) => tracked.push({ name, props }) }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => "org-1" }));
// The org route's org, when a test switches orgs (undefined: the tab's org, org-1).
const routeOrg = vi.hoisted(() => ({ current: undefined as string | undefined }));
vi.mock("@/lib/orgScope", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/orgScope")>()), useRouteOrgIdOverride: () => routeOrg.current }));
vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));

import { MlGatedView } from "../MlRoute";

const NO_DATA = { kind: "no-data" } as const;

function api(over: Partial<MlApi> & { entitlement?: OrgEntitlement } = {}): MlApi {
  const { entitlement = { feature: "ml", enabled: false, requestedAtMs: null, grantedAtMs: null }, ...rest } = over;
  const none = async () => NO_DATA as MlOutcome<never>;
  return {
    getMyOrgEntitlements: vi.fn(async () => [entitlement]),
    requestOrgEntitlement: vi.fn(async () => ({ entitlement: { ...entitlement, requestedAtMs: Date.UTC(2026, 10, 25) }, alreadyRequested: false, alreadyEnabled: false })),
    read: none as MlApi["read"],
    ...rest,
  };
}

function renderGate(mlApi: MlApi, view: Parameters<typeof MlGatedView>[0]["view"] = "fleet") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const ui = () => (
    <QueryClientProvider client={client}>
      <MlApiProvider api={mlApi}>
        <MlGatedView view={view} />
      </MlApiProvider>
    </QueryClientProvider>
  );
  const r = render(ui());
  /** Re-render on another org, as the org route does on a switch. */
  const switchOrg = (orgId: string) => {
    routeOrg.current = orgId;
    r.rerender(ui());
  };
  return { client, switchOrg };
}

const ENABLED: OrgEntitlement = { feature: "ml", enabled: true, requestedAtMs: null, grantedAtMs: 1 };
const DISABLED: OrgEntitlement = { feature: "ml", enabled: false, requestedAtMs: null, grantedAtMs: null };

beforeEach(() => {
  tracked.length = 0;
  routeOrg.current = undefined;
});
afterEach(() => vi.clearAllMocks());

describe("the ML gate", () => {
  it("shows the access page to an org without ml, and records the view once", async () => {
    const mlApi = api();
    renderGate(mlApi, "runs");
    expect(await screen.findByTestId("ml-access-page")).toHaveAttribute("data-requested", "false");
    expect(screen.getByRole("heading", { name: "Reach out to Reliant for access" })).toBeInTheDocument();
    expect(screen.getByTestId("ml-access-demo")).toHaveAttribute("href", "https://demo.reliantlabs.io/forge/ml/overview");
    expect(mlApi.getMyOrgEntitlements).toHaveBeenCalledWith("org-1", expect.anything());
    expect(tracked).toEqual([{ name: "ml_access_page_viewed", props: { org_id: "org-1", view: "runs", source: "entitlement", requested: false } }]);
  });

  it("requests access, and says when", async () => {
    const mlApi = api();
    renderGate(mlApi);
    fireEvent.click(await screen.findByTestId("ml-access-request"));
    expect(await screen.findByTestId("ml-access-requested")).toHaveTextContent(/^Requested on .*2026; we'll reach out\.$/);
    expect(mlApi.requestOrgEntitlement).toHaveBeenCalledWith("org-1", "ml", undefined);
    expect(screen.getByTestId("ml-access-request")).toBeDisabled();
    // The server emits ml_access_requested; the page never sends a second copy.
    expect(tracked.map((e) => e.name)).toEqual(["ml_access_page_viewed"]);
  });

  it("shows an earlier request, with the button disabled", async () => {
    renderGate(api({ entitlement: { feature: "ml", enabled: false, requestedAtMs: Date.UTC(2026, 10, 20), grantedAtMs: null } }));
    expect(await screen.findByTestId("ml-access-requested")).toBeInTheDocument();
    expect(screen.getByTestId("ml-access-request")).toBeDisabled();
  });

  it("offers the support email when the request fails", async () => {
    renderGate(api({ requestOrgEntitlement: vi.fn(async () => Promise.reject(new Error("down"))) }));
    fireEvent.click(await screen.findByTestId("ml-access-request"));
    const mailto = await screen.findByTestId("ml-access-mailto");
    expect(mailto.getAttribute("href")).toMatch(/^mailto:support@reliantlabs\.io\?subject=ML%20early%20access&body=.*org-1/);
  });

  it("turns a FEATURE_NOT_ENTITLED read into the access page", async () => {
    renderGate(api({ entitlement: { feature: "ml", enabled: true, requestedAtMs: null, grantedAtMs: 1 }, read: (async () => ({ kind: "not-entitled" })) as MlApi["read"] }), "overview");
    expect(await screen.findByTestId("ml-access-page")).toBeInTheDocument();
    await waitFor(() => expect(tracked.at(-1)?.props?.source).toBe("refused"));
  });

  it("opens the pages again when a revoked org is granted ML again", async () => {
    const server = { entitlement: ENABLED, refuse: false };
    const mlApi = api({
      getMyOrgEntitlements: vi.fn(async () => [server.entitlement]),
      read: vi.fn(async () => (server.refuse ? { kind: "not-entitled" as const } : NO_DATA)) as unknown as MlApi["read"],
    });
    const { client } = renderGate(mlApi, "setup");
    expect(await screen.findByTestId("ml-no-data")).toBeInTheDocument();

    // Revoked: the next ML read refuses, and the entitlement re-read agrees.
    server.entitlement = DISABLED;
    server.refuse = true;
    await act(() => client.invalidateQueries({ queryKey: [...mlKeys.all("org-1"), "read"] }));
    expect(await screen.findByTestId("ml-access-page")).toBeInTheDocument();
    await waitFor(() => expect(mlApi.getMyOrgEntitlements).toHaveBeenCalledTimes(2));

    // Granted again: the next entitlement read opens the pages, which read afresh.
    server.entitlement = { ...ENABLED, grantedAtMs: 2 };
    server.refuse = false;
    await act(() => client.invalidateQueries({ queryKey: mlKeys.entitlements("org-1") }));
    expect(await screen.findByTestId("ml-no-data")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });

  it("keeps a refusal to its org: switching to an entitled org opens its pages", async () => {
    routeOrg.current = "org-a";
    // org-a's entitlement says enabled, but its ML reads refuse: the latch holds for org-a.
    const mlApi = api({
      getMyOrgEntitlements: vi.fn(async () => [ENABLED]),
      read: vi.fn(async (orgId: string) => (orgId === "org-a" ? { kind: "not-entitled" as const } : NO_DATA)) as unknown as MlApi["read"],
    });
    const { switchOrg } = renderGate(mlApi, "setup");
    expect(await screen.findByTestId("ml-access-page")).toBeInTheDocument();
    await waitFor(() => expect(mlApi.getMyOrgEntitlements).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("ml-access-page")).toBeInTheDocument();

    switchOrg("org-b");
    expect(await screen.findByTestId("ml-no-data")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });

  it("opens the pages when a request answers already enabled", async () => {
    const server = { refuse: true };
    const mlApi = api({
      entitlement: ENABLED,
      read: vi.fn(async () => (server.refuse ? { kind: "not-entitled" as const } : NO_DATA)) as unknown as MlApi["read"],
      requestOrgEntitlement: vi.fn(async () => ({ entitlement: ENABLED, alreadyRequested: false, alreadyEnabled: true })),
    });
    renderGate(mlApi, "setup");
    expect(await screen.findByTestId("ml-access-page")).toBeInTheDocument();
    await waitFor(() => expect(mlApi.getMyOrgEntitlements).toHaveBeenCalledTimes(2));

    server.refuse = false;
    fireEvent.click(screen.getByTestId("ml-access-request"));
    expect(await screen.findByTestId("ml-no-data")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });

  it("keeps a request made for one org off another org's access page", async () => {
    routeOrg.current = "org-b";
    const mlApi = api({
      requestOrgEntitlement: vi.fn(async () => ({ entitlement: { ...DISABLED, requestedAtMs: Date.UTC(2026, 10, 25) }, alreadyRequested: false, alreadyEnabled: false })),
    });
    const { switchOrg } = renderGate(mlApi);
    expect(await screen.findByTestId("ml-access-page")).toHaveAttribute("data-requested", "false");

    switchOrg("org-a");
    await waitFor(() => expect(mlApi.getMyOrgEntitlements).toHaveBeenCalledWith("org-a", expect.anything()));
    fireEvent.click(await screen.findByTestId("ml-access-request"));
    expect(await screen.findByTestId("ml-access-requested")).toBeInTheDocument();
    expect(mlApi.requestOrgEntitlement).toHaveBeenCalledWith("org-a", "ml", undefined);

    // Back to org-b, whose entitlement is cached: it never asked.
    switchOrg("org-b");
    expect(await screen.findByTestId("ml-access-page")).toHaveAttribute("data-requested", "false");
    expect(screen.queryByTestId("ml-access-requested")).toBeNull();
    expect(screen.getByTestId("ml-access-request")).toBeEnabled();
    expect(screen.getByTestId("ml-access-request")).toHaveTextContent("Request access");
  });

  it("shows the empty state, not rows, when the data does not exist yet", async () => {
    renderGate(api({ entitlement: { feature: "ml", enabled: true, requestedAtMs: null, grantedAtMs: 1 } }), "setup");
    expect(await screen.findByTestId("ml-no-data")).toHaveTextContent("ML data appears here once a cluster is connected");
    expect(screen.getByTestId("ml-page-setup")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });

  it("labels Training, Evals, Models and Inference Early access in their header", async () => {
    renderGate(api({ entitlement: { feature: "ml", enabled: true, requestedAtMs: null, grantedAtMs: 1 } }), "models");
    const page = await screen.findByTestId("ml-page-models");
    expect(page).toHaveTextContent("Early access");
  });
});
