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
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const listSecretsRpc = vi.fn();
const setSecretRpc = vi.fn();

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    listSecrets: listSecretsRpc,
    setSecret: setSecretRpc,
  }),
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://localhost:8090",
}));

import {
  listSecrets,
  managedStoreTarget,
  normalizeEndpoint,
  setSecret,
} from "../secretStore";

beforeEach(() => {
  listSecretsRpc.mockReset().mockResolvedValue({ secrets: [{ name: "B" }, { name: "A" }] });
  setSecretRpc.mockReset().mockResolvedValue({ version: 3 });
});

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

  it.each(["cluster", "compose", "host", "external", "static", "mixed", undefined, "moon-base"])(
    "gives a %s env no lookup (no managed store, no fake id)",
    (destination) => {
      expect(managedStoreTarget({ destination, environment_id: "denv_should_be_ignored" })).toEqual({
        kind: "none",
        availability: "not-hosted",
      });
    }
  );

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

describe("the RPCs are keyed by environmentId", () => {
  it("lists with environmentId and nothing else", async () => {
    const rows = await listSecrets("denv_1");
    expect(listSecretsRpc).toHaveBeenCalledWith({ environmentId: "denv_1" });
    expect(rows.map((r) => r.name)).toEqual(["A", "B"]);
  });

  it("writes with environmentId", async () => {
    await setSecret({ environmentId: "denv_1", name: "K", value: "v", cas: 0 });
    expect(setSecretRpc).toHaveBeenCalledWith({ environmentId: "denv_1", name: "K", secretValue: "v", cas: 0 });
  });
});
