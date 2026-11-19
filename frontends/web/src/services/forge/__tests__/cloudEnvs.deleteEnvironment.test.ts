// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/** keep_data is sent only to ask for it; false is the unset default, never a field on the wire. */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

const deleteEnvironmentRpc = vi.fn();

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({ deleteEnvironment: (req: unknown) => deleteEnvironmentRpc(req) }),
}));

import { deleteEnvironment, isUnimplemented } from "../cloudEnvs";

beforeEach(() => {
  deleteEnvironmentRpc.mockReset().mockResolvedValue({});
});

describe("deleteEnvironment", () => {
  it("omits keep_data when the data is to be deleted", async () => {
    await deleteEnvironment("env_1");
    await deleteEnvironment("env_1", { keepData: false });
    for (const [req] of deleteEnvironmentRpc.mock.calls) {
      expect(req).toEqual({ environmentId: "env_1", force: true });
      expect(req).not.toHaveProperty("keepData");
    }
  });

  it("sends keep_data only when asked to keep", async () => {
    await deleteEnvironment("env_1", { keepData: true });
    expect(deleteEnvironmentRpc).toHaveBeenCalledWith({ environmentId: "env_1", force: true, keepData: true });
  });
});

describe("isUnimplemented", () => {
  it("is true only for Unimplemented", () => {
    expect(isUnimplemented(new ConnectError("no", Code.Unimplemented))).toBe(true);
    expect(isUnimplemented(new ConnectError("no", Code.PermissionDenied))).toBe(false);
    expect(isUnimplemented(new Error("no"))).toBe(false);
  });
});
