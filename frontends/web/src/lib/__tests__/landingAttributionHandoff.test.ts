import { beforeEach, describe, expect, it, vi } from "vitest";
import HyperDXModule from "@hyperdx/browser";

// The landing → app handoff end to end, through the REAL forge web runtime (only
// the HyperDX SDK underneath it is faked). The landing site links to the app with
// `?rid=<its visitor id>&utm_*` (landing-page src/lib/attribution.ts); a sign-in
// then replaces the URL before this app starts telemetry. The visitor must still
// be identified as the landing visitor: `user_identified` carries the landing's
// visitor id and campaign, which is how product-funnel joins a visit to a user.
// This failed in prod until the boot hold: 0 of 12 user_identified matched.
//
// The runtime is a dependency vitest does not transform, so vi.mock cannot reach
// its import of @hyperdx/browser: the test spies on that same SDK singleton
// instead, with init stubbed so nothing leaves the process.

type SDK = {
  init: (...a: unknown[]) => void;
  setGlobalAttributes: (a: Record<string, string>) => void;
  addAction: (name: string, attrs?: Record<string, string>) => void;
};
function client(mod: unknown): SDK {
  let cur = mod as { default?: unknown };
  while (cur && typeof (cur as SDK).init !== "function" && cur.default) cur = cur.default as { default?: unknown };
  return cur as unknown as SDK;
}
const hdx = client(HyperDXModule);
const sdk = {
  init: vi.spyOn(hdx, "init").mockImplementation(() => {}),
  setGlobalAttributes: vi.spyOn(hdx, "setGlobalAttributes").mockImplementation(() => {}),
  addAction: vi.spyOn(hdx, "addAction").mockImplementation(() => {}),
};
beforeEach(() => vi.clearAllMocks());

import { setTelemetryUser } from "@reliantlabs/forge-web-runtime";
import { enableAttribution, initBrowserTelemetry } from "@reliantlabs/forge-web-runtime/telemetry";
import { holdLandingAttribution, takeLandingAttribution } from "../landingAttribution";

describe("landing → app attribution handoff (real runtime)", () => {
  it("identifies the signed-in user as the landing visitor after the sign-in replaced the URL", async () => {
    localStorage.clear();
    // 1. The landing link lands on the app; the boot holds it (attributionBoot.ts).
    window.history.replaceState(null, "", "/?rid=v_landing_123456&utm_source=reddit&utm_campaign=b2");
    holdLandingAttribution();
    // 2. Signed out: the router sends the visitor to sign-in, and the OAuth
    //    callback is a page load of its own. The address bar loses the handoff.
    window.history.replaceState(null, "", "/auth/callback?code=abc");
    // 3. Telemetry starts after sign-in (telemetry.ts start()).
    const handoff = takeLandingAttribution();
    expect(handoff).toBeDefined();
    enableAttribution({ search: handoff });
    await expect(
      initBrowserTelemetry({ serviceName: "reliant-web", endpoint: "https://ingest.example.dev/k/rbk_x", appClient: "web" }),
    ).resolves.toBe(true);
    // 4. The session's user is attached: the runtime emits user_identified once.
    setTelemetryUser("user-1");

    expect(sdk.init).toHaveBeenCalledTimes(1);
    const globals = Object.assign({}, ...sdk.setGlobalAttributes.mock.calls.map((c) => c[0]));
    expect(globals["visitor.id"]).toBe("v_landing_123456");
    const identified = sdk.addAction.mock.calls.filter(([name]) => name === "user_identified");
    expect(identified).toHaveLength(1);
    expect(identified[0][1]).toMatchObject({
      visitor_id: "v_landing_123456",
      utm_source: "reddit",
      utm_campaign: "b2",
    });
  });
});
