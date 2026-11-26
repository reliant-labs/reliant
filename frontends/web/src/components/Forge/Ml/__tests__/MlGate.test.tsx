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
 *   - an entitled org on a control plane without the data gets the calm
 *     empty state, never invented rows.
 */

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { MlApiProvider, type MlApi } from "@/services/controlPlane/ml/MlApiContext";
import type { MlOutcome, OrgEntitlement } from "@/services/controlPlane/ml/types";

const tracked = vi.hoisted(() => [] as { name: string; props?: Record<string, unknown> }[]);
vi.mock("@/lib/analytics", () => ({ trackEvent: (name: string, props?: Record<string, unknown>) => tracked.push({ name, props }) }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => "org-1" }));
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
  render(
    <QueryClientProvider client={client}>
      <MlApiProvider api={mlApi}>
        <MlGatedView view={view} />
      </MlApiProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  tracked.length = 0;
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
