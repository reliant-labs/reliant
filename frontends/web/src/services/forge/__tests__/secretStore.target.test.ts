// Copyright (c) 2025 Reliant Labs

/**
 * The managed store is keyed by a CONTROL-PLANE environment id, and the only
 * honest source of that id is forge's topology report. These tests pin:
 *
 *   - a hosted, ensured env calls SecretStoreService with `environmentId`;
 *   - a non-hosted env (or one forge never classified) makes NO call at all;
 *   - a hosted env with no id makes no call and says why, rather than sending
 *     an empty or made-up id;
 *   - a hosted env on a DIFFERENT control plane makes no call here.
 *
 * The requests a lookup target keys (ListSecrets, SetSecret with its
 * `environmentId`) are pinned by hooks/__tests__/forge-queries.managedSecrets.test.tsx.
 */

import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://localhost:8090",
}));

import { managedStoreTarget, normalizeEndpoint } from "../secretStore";

describe("managedStoreTarget", () => {
  it("targets a hosted, ensured env on this console's control plane by its environment id", () => {
    expect(
      managedStoreTarget({
        destination: "hosted",
        endpoint: "http://127.0.0.1:8090",
        environment_id: "denv_1",
      })
    ).toEqual({ kind: "lookup", environmentId: "denv_1", endpoint: "http://127.0.0.1:8090" });
  });

  it.each(["cluster", "compose", "host", "external", "static", "mixed"])(
    "gives a %s env no lookup (no managed store, no fake id)",
    (destination) => {
      expect(managedStoreTarget({ destination, environment_id: "denv_should_be_ignored" })).toEqual({
        kind: "none",
        availability: "not-hosted",
      });
    }
  );

  // forge named NO destination (an older forge, a KCL render that failed) or
  // one this build does not know. Still no lookup — but not "not-hosted"
  // either: nothing said the env uses a different provider.
  it.each([undefined, "", "moon-base"])(
    "gives a %s destination no lookup and calls the provider unknown, not not-hosted",
    (destination) => {
      expect(managedStoreTarget({ destination, environment_id: "denv_should_be_ignored" })).toEqual({
        kind: "none",
        availability: "provider-unknown",
      });
    }
  );

  it("calls the provider unknown when there is no forge report at all", () => {
    expect(managedStoreTarget(null)).toEqual({ kind: "none", availability: "provider-unknown" });
  });

  it("refuses to look up a hosted env that was never ensured", () => {
    expect(managedStoreTarget({ destination: "hosted", endpoint: "http://localhost:8090", environment_id: "" })).toEqual({
      kind: "none",
      availability: "not-ensured",
    });
  });

  it("refuses to send another control plane's id to this one", () => {
    expect(
      managedStoreTarget({ destination: "hosted", endpoint: "https://api.reliantlabs.io", environment_id: "denv_1" })
    ).toEqual({ kind: "none", availability: "other-control-plane" });
  });

  it("says no-control-plane when this console has none, before anything else", () => {
    expect(managedStoreTarget({ destination: "hosted", environment_id: "denv_1" }, "")).toEqual({
      kind: "none",
      availability: "no-control-plane",
    });
  });

  it("treats localhost and 127.0.0.1 as the same control plane", () => {
    expect(normalizeEndpoint("http://localhost:8090/")).toBe(normalizeEndpoint("http://127.0.0.1:8090"));
  });
});
