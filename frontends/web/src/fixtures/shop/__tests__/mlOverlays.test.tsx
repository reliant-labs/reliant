// Copyright (c) 2026 Reliant Labs

/**
 * The ML access overlays (shop/overlays.ts `ml-not-entitled`, `ml-requested`;
 * the hosted preview's `?fixtures=ml-not-entitled`), served as fixture mode
 * serves them: over the app pack, through the read-only fixture transport.
 * The org has no `ml`, every ML read is refused FEATURE_NOT_ENTITLED, and the
 * real gate (MlRoute MlGatedView) renders the "Reach out to Reliant for
 * access" page; asking for access is still refused, as every demo write is.
 */

import { createClient } from "@connectrpc/connect";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { createAppQueryClient } from "@/lib/query-client";
import { hasErrorReason } from "@/lib/errorReasons";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import { createFixtureTransport, defineFixturePack } from "../../transport";
import { shopAppPack } from "../app";
import { SHOP_READ_ONLY_MESSAGE } from "../index";
import { ML_NOT_ENTITLED_MESSAGE, SHOP_OVERLAYS } from "../overlays";
import { DAY, DEMO_ORG, demoNow, resetDemoNowForTests } from "../shopStory";

vi.mock("@/lib/analytics", () => ({ trackEvent: () => {} }));
vi.mock("@/services/controlPlane/orgs", () => ({ currentOrgId: async () => DEMO_ORG }));
vi.mock("@/components/Forge/Observability/charts/UPlotChart", () => ({ UPlotChart: () => <figure data-testid="chart" /> }));

import { MlGatedView } from "@/components/Forge/Ml/MlRoute";

const NOW = Date.UTC(2026, 10, 18, 15, 30);

/** The app pack with `overlays` on top, as install.ts composes `?fixtures=`. */
function served(...overlays: string[]) {
  const app = shopAppPack({ now: NOW });
  const pack = defineFixturePack({ name: "test", extends: [app, ...overlays.map((name) => SHOP_OVERLAYS[name]())] });
  return createFixtureTransport({ pack, now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
}

function renderGate(...overlays: string[]) {
  const scope = createTransportScope({ id: `ml-${overlays.join("+")}`, transport: served(...overlays), queryClient: createAppQueryClient(), readOnly: true });
  scope.queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <TransportScopeProvider scope={scope}>
      <MlGatedView view="fleet" />
    </TransportScopeProvider>,
  );
}

beforeEach(() => resetDemoNowForTests());
afterEach(() => {
  cleanup();
  resetDemoNowForTests();
});

describe("the ML access overlays", () => {
  it("ml-not-entitled: ml disabled, never asked for, and every ML read refused FEATURE_NOT_ENTITLED", async () => {
    const transport = served("ml-not-entitled");
    const { features } = await createClient(OrganizationService, transport).getMyOrgEntitlements({ orgId: DEMO_ORG });
    expect(features).toHaveLength(1);
    expect(features[0]).toMatchObject({ feature: "ml", enabled: false });
    expect(features[0].requestedAt).toBeUndefined();
    expect(features[0].grantedAt).toBeUndefined();
    const ml = createClient(MlEarlyAccessService, transport);
    for (const read of [() => ml.getMlFleet({ orgId: DEMO_ORG }), () => ml.listMlRuns({ orgId: DEMO_ORG }), () => ml.listMlInferenceServices({ orgId: DEMO_ORG })]) {
      const error = await read().then(
        () => null,
        (e: unknown) => e,
      );
      expect(hasErrorReason(error, "FEATURE_NOT_ENTITLED")).toBe(true);
      expect(String(error)).toContain(ML_NOT_ENTITLED_MESSAGE);
    }
    await expect(createClient(OrganizationService, transport).requestOrgEntitlement({ orgId: DEMO_ORG, feature: "ml" })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("ml-not-entitled: the gate renders the access page", async () => {
    renderGate("ml-not-entitled");
    const page = await screen.findByTestId("ml-access-page");
    expect(page).toHaveTextContent("Reach out to Reliant for access");
    expect(page).toHaveAttribute("data-requested", "false");
    expect(screen.queryByTestId("ml-page-fleet")).toBeNull();
  });

  it("ml-requested: asked for 3 days ago, and the access page says so", async () => {
    const { features } = await createClient(OrganizationService, served("ml-requested")).getMyOrgEntitlements({ orgId: DEMO_ORG });
    expect(features[0]).toMatchObject({ feature: "ml", enabled: false });
    expect(Number(features[0].requestedAt!.seconds) * 1000).toBe(NOW - 3 * DAY);
    renderGate("ml-requested");
    expect(await screen.findByTestId("ml-access-page")).toHaveAttribute("data-requested", "true");
    expect(screen.getByTestId("ml-access-requested")).toHaveTextContent(/^Requested on .*; we'll reach out\.$/);
  });

  it("without an overlay, the org has ML and the pages show", async () => {
    renderGate();
    expect(await screen.findByTestId("ml-page-fleet")).toBeInTheDocument();
    expect(screen.queryByTestId("ml-access-page")).toBeNull();
  });
});
