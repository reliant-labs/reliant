// @vitest-environment node
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { controlPlaneObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { DEMO_READ_ONLY_REASON, QUIET_WRITES, READ_METHODS, WRITE_METHODS, withReadOnly } from "../readOnlyApi";

describe("withReadOnly", () => {
  it("classifies every method of the ObservabilityApi exactly once", () => {
    const classified = [...READ_METHODS, ...WRITE_METHODS, ...Object.keys(QUIET_WRITES)];
    // No method in two lists…
    expect(new Set(classified).size).toBe(classified.length);
    // …and none left out: a method added without a decision fails here.
    expect([...classified].sort()).toEqual(Object.keys(controlPlaneObservabilityApi).sort());
  });

  it("refuses every write with PermissionDenied and the demo's reason, before the API is reached", async () => {
    const api = withReadOnly(createFixtureApi());
    for (const method of WRITE_METHODS) {
      const call = (api[method] as (...args: unknown[]) => Promise<unknown>)({}, {}, {});
      await expect(call, method).rejects.toSatisfy((err: unknown) => err instanceof ConnectError && err.code === Code.PermissionDenied && err.rawMessage === DEMO_READ_ONLY_REASON);
    }
  });

  it("names its own reason when given one", async () => {
    const api = withReadOnly(createFixtureApi(), "Read-only demo");
    await expect(api.updateIssueStatus({} as never)).rejects.toThrow("Read-only demo");
  });

  it("answers bookkeeping writes as no-ops, and passes reads through", async () => {
    const fixtures = createFixtureApi();
    const api = withReadOnly(fixtures);
    await expect(api.recordDashboardView("org", {} as never)).resolves.toBeUndefined();
    await expect(api.markNotificationsRead("org")).resolves.toBe(0);
    for (const method of READ_METHODS) expect(api[method], method).toBe(fixtures[method]);
  });
});
