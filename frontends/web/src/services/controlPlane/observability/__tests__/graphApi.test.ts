// Copyright (c) 2026 Reliant Labs
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { GetServiceGraphResponseSchema, GetServiceGraphCatalogResponseSchema, GraphEdgeKind, GraphNodeKind, GraphEvidenceKind } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { getServiceGraph, getServiceGraphCatalog } from "../graphApi";
const client = { getServiceGraph: vi.fn(), getServiceGraphCatalog: vi.fn() };
vi.mock("../../client", () => ({ getControlPlaneClient: () => client }));
const scope = { orgId: "org", projectId: "shop", envIds: ["env"], filters: [], window: { startMs: 60000, endMs: 120000 } };
beforeEach(() => vi.clearAllMocks());
describe("actual CP graph contract", () => {
  it("sends exact scoped service AND method filters through existing authenticated client", async () => {
    client.getServiceGraph.mockResolvedValue(create(GetServiceGraphResponseSchema));
    await getServiceGraph({ ...scope, rpcIds: ["shop.v1.Orders/Get"], rpcService: "shop.v1.Orders", includeInternal: true });
    expect(client.getServiceGraph).toHaveBeenCalledWith(expect.objectContaining({ scope: expect.objectContaining({ orgId: "org", projectId: "shop", envIds: ["env"] }), rpcIds: ["shop.v1.Orders/Get"], rpcService: "shop.v1.Orders", maxEdges: 500, includeInternal: true }), expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });
  it("preserves exact counters and unknown enum/coverage states", async () => {
    client.getServiceGraph.mockResolvedValue(create(GetServiceGraphResponseSchema, {
      nodes: [{ id: "browser", kind: GraphNodeKind.FRONTEND }],
      edges: [{ id: "e", kind: GraphEdgeKind.ASYNC_CONSUME, evidence: GraphEvidenceKind.OBSERVED, count: 9007199254740993n, unmatchedCount: 3n }],
    }));
    const result = await getServiceGraph({ ...scope, rpcIds: [], includeInternal: false });
    expect(result.nodes[0].kind).toBe("frontend");
    expect(result.edges[0]).toMatchObject({ kind: "async-consume", count: 9007199254740993n, unmatched: 3n });
    expect(result.quality).toBeNull();
    expect(result.window).toBeNull();
  });
  it("missing verified metadata stays unavailable; never fabricated schemas", async () => {
    client.getServiceGraphCatalog.mockResolvedValue(create(GetServiceGraphCatalogResponseSchema, { declaredCatalogStatus: "unavailable", declaredCatalogUnavailableReason: "No verified catalog", methods: [{ rpcId: "shop.v1.Orders/Get" }] }));
    const c = await getServiceGraphCatalog(scope);
    expect(c.declaredCatalogs).toHaveLength(0);
    expect(c.unavailableReason).toBe("No verified catalog");
    expect(c.methods[0].authRequired).toBeUndefined();
    expect(c.methods[0].evidence).toBe("unknown");
  });
  it("preserves all bounded artifacts, including a disagreeing 21st catalog", async () => {
    client.getServiceGraphCatalog.mockResolvedValue(create(GetServiceGraphCatalogResponseSchema, { declaredCatalogStatus: "available",
      declaredCatalogs: Array.from({ length: 21 }, (_, i) => ({ envId: `env-${i}`, bundleDigest: `bundle-${i}`, catalogJson: `catalog-${i}` })),
    }));
    const c = await getServiceGraphCatalog({ ...scope, rpcIds: ["shop.v1.Orders/Get201"] });
    expect(c.declaredCatalogs).toHaveLength(21);
    expect(c.declaredCatalogs[20].json).toBe("catalog-20");
    expect(client.getServiceGraphCatalog).toHaveBeenCalledWith(expect.objectContaining({ rpcIds: ["shop.v1.Orders/Get201"], maxMethods: 500 }), expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });
  it("does not convert authorization/backend failure into empty success", async () => {
    const error = new ConnectError("denied", Code.PermissionDenied);
    client.getServiceGraph.mockRejectedValue(error);
    await expect(getServiceGraph({ ...scope, rpcIds: [], includeInternal: false })).rejects.toBe(error);
  });
});
