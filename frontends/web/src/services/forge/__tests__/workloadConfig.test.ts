// Copyright (c) 2025 Reliant Labs
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GetWorkloadConfigResponseSchema, DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { configReadError, getWorkloadConfig, validateWorkloadConfig, type WorkloadConfig } from "../workloadConfig";

vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: vi.fn() }));
const rpc = vi.fn();
const request = { environmentId: "env-prod", workload: "api" };
const result: WorkloadConfig = { ...request, bundleId: "bundle", bundleDigest: `sha256:${"b".repeat(64)}`, releaseVersion: "v2", objects: [], renderedYaml: "", frontendConfigUnsupportedReason: "unsupported" };
beforeEach(() => {
  rpc.mockReset();
  vi.mocked(getControlPlaneClient).mockReturnValue({ getWorkloadConfig: rpc } as unknown as ReturnType<typeof getControlPlaneClient>);
});

describe("config wire adapter", () => {
  it("uses the existing typed service with optional selectors absent for current and forwards cancellation", async () => {
    rpc.mockResolvedValue(create(GetWorkloadConfigResponseSchema, result));
    const signal = new AbortController().signal;
    expect(await getWorkloadConfig(request, signal)).toEqual(result);
    expect(getControlPlaneClient).toHaveBeenCalledWith(DeployService);
    expect(rpc).toHaveBeenCalledWith({ ...request, bundleId: undefined, releaseVersion: undefined }, { signal });
  });
  it("pins the explicit organization in the typed request before authentication/retry", async () => {
    rpc.mockResolvedValue(create(GetWorkloadConfigResponseSchema, result));
    const signal = new AbortController().signal;
    await getWorkloadConfig({ ...request, orgId: "org-selected" }, signal);
    expect(rpc).toHaveBeenCalledWith({ ...request, bundleId: undefined, releaseVersion: undefined, orgId: "org-selected" }, { signal });
  });
  it("maps Struct objects directly as JSON without parsing HTML, YAML or downloading artifacts", async () => {
    const objects = [{ data: { text: "<script>hostile</script>\nline", null: null } }];
    rpc.mockResolvedValue(create(GetWorkloadConfigResponseSchema, { ...result, objects, renderedYaml: "redacted" }));
    expect((await getWorkloadConfig({ ...request, bundleId: "bundle" }, new AbortController().signal)).objects).toEqual(objects);
  });
  it("rejects null/missing, wrong scope and selector mismatches", async () => {
    for (const response of [null, undefined, { ...result, environmentId: "env-other" }, { ...result, workload: "other" }, { ...result, bundleId: "" }]) {
      rpc.mockResolvedValue(response);
      await expect(getWorkloadConfig(request, new AbortController().signal)).rejects.toThrow();
    }
    expect(() => validateWorkloadConfig(result, { ...request, bundleId: "other" })).toThrow();
    expect(() => validateWorkloadConfig(result, { ...request, releaseVersion: "v1" })).toThrow();
  });
  it("allows legitimate image/bundle digests and fixed withheld markers without secret comparison", () => {
    expect(validateWorkloadConfig({ ...result, renderedYaml: 'image: registry/api@sha256:abc\nTOKEN: "forge.dev/redacted: withheld"', objects: [{ image: "registry/api@sha256:abc", TOKEN: "forge.dev/redacted: withheld" }] }, request)).toBeTruthy();
  });
  it("refuses legacy secret fingerprints in either encoding before display or export", () => {
    expect(() => validateWorkloadConfig({ ...result, renderedYaml: 'TOKEN: "forge.dev/redacted: sha256:abc"' }, request)).toThrow();
    expect(() => validateWorkloadConfig({ ...result, objects: [{ data: { TOKEN: "forge.dev/redacted: sha256:abc" } }] }, request)).toThrow();
  });
  it("does not send empty selectors, empty scope or dual selectors", async () => {
    await expect(getWorkloadConfig({ ...request, bundleId: "" }, new AbortController().signal)).rejects.toThrow();
    await expect(getWorkloadConfig({ ...request, workload: "" }, new AbortController().signal)).rejects.toThrow();
    // Exercise runtime callers that bypass the mutually-exclusive TS union.
    await expect(getWorkloadConfig({ ...request, bundleId: "bundle", releaseVersion: "v1" } as never, new AbortController().signal)).rejects.toThrow();
    expect(rpc).not.toHaveBeenCalled();
  });
  it("preserves old-server and grant refusals, without echoing arbitrary payloads", async () => {
    for (const code of [Code.Unimplemented, Code.PermissionDenied]) {
      rpc.mockRejectedValue(new ConnectError("raw bytes", code));
      await expect(getWorkloadConfig(request, new AbortController().signal)).rejects.toMatchObject({ code });
      expect(configReadError(new ConnectError("raw bytes", code))).not.toContain("raw bytes");
    }
  });
});
