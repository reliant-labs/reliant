// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * The analysis behind the org-scoped query guard, on a source tree written
 * here: a fake service with one org-scoped rpc (its request has org_id) and
 * one that is not, and a read of each shape the guard must tell apart.
 */

import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterAll, describe, expect, it } from "vitest";

import { OrgScopeAnalysis, type Site } from "../orgScopeAnalysis";

const root = mkdtempSync(join(tmpdir(), "org-scope-analysis-"));
afterAll(() => rmSync(root, { recursive: true, force: true }));

function write(path: string, text: string) {
  mkdirSync(dirname(join(root, path)), { recursive: true });
  writeFileSync(join(root, path), text);
}

write("gen/fake_pb.ts", "export const FakeService = {};\n");
write(
  "hooks/fake-service-hooks_gen.ts",
  `import { FakeService } from "@/gen/fake_pb";
const client = connectClient(FakeService);
export const fakeServiceQueries = { listThings: 1, getThing: 2 };
export const useListThings = createQueryHook(fakeServiceQueries.listThings);
export const useGetThing = createQueryHook(fakeServiceQueries.getThing);
`,
);
write(
  "api.ts",
  `import { FakeService } from "@/gen/fake_pb";
function client() { return getControlPlaneClient(FakeService); }
export async function listThings(orgId: string | null) { return client().listThings(orgRequest({}, orgId)); }
export async function listAnyThings() { return client().listThings({}); }
`,
);
// An api module whose import name shadows a browser global (history).
write(
  "historyApi.ts",
  `import { FakeService } from "@/gen/fake_pb";
export function getItem(req: object) { return connectClient(FakeService).getItem(req); }
`,
);
write(
  "historyReads.ts",
  `import * as history from "./historyApi";
export function useHistoryNamespace() { return useQuery({ queryKey: ["h"], queryFn: () => history.getItem({}) }); }
`,
);
write(
  "historyPackageReads.ts",
  `import { history } from "some-router";
export function useHistoryPackage() { return useQuery({ queryKey: ["h"], queryFn: () => history.getItem({}) }); }
`,
);
write(
  "reads.ts",
  `import { useGetThing, useListThings, fakeServiceQueries } from "./hooks/fake-service-hooks_gen";
import { listAnyThings, listThings } from "./api";
export function useUnscoped() { return useListThings({}); }
export function useScoped() { const org = useOrgScope(); return useListThings(orgRequest({}, org.orgId), { enabled: orgQueryEnabled(org) }); }
export function useNotOrgScoped() { return useGetThing({ id: "x" }); }
export function useKeeps() { const org = useOrgScope(); return useListThings(orgRequest({}, org.orgId), { enabled: orgQueryEnabled(org), placeholderData: keepPreviousData }); }
export function useCacheOnly() { const org = useOrgScope(); return useListThings(orgRequest({}, org.orgId), { enabled: false }); }
export function keyOf(orgId: string) { return fakeServiceQueries.listThings(orgRequest({}, orgId)).queryKey; }
export function useHandUnscoped() { return useQuery({ queryKey: ["things"], queryFn: () => listAnyThings() }); }
export function useHandScoped(orgId: string | null) { return useQuery({ queryKey: ["things", orgId], queryFn: () => listThings(orgId), enabled: !!orgId }); }
export function useHandFetchesElsewhere(orgId: string | null) { return useQuery({ queryKey: ["things", orgId], queryFn: () => listAnyThings(), enabled: !!orgId }); }
export function useStorageOnly() { return useQuery({ queryKey: ["s"], queryFn: () => window.sessionStorage.getItem("k") ?? localStorage.getItem("k") }); }
export function useStorageAlias() { const s = window.localStorage; return useQuery({ queryKey: ["s"], queryFn: () => s.getItem("k") }); }
export function useWrapped(api: { getItem(r: object): unknown }) { return useQuery({ queryKey: ["i"], queryFn: () => api.getItem({}) }); }
export async function fetchThings() { const { orgId } = await whenOrgScopeReady(); return queryClient.fetchQuery(fakeServiceQueries.listThings(orgRequest({}, orgId))); }
`,
);

const fakeService = {
  kind: "service",
  typeName: "fake.v1.FakeService",
  methods: [
    { localName: "listThings", methodKind: "unary", input: { fields: [{ name: "org_id" }] } },
    { localName: "getThing", methodKind: "unary", input: { fields: [{ name: "id" }] } },
    // Named like a DOM method (Storage.getItem).
    { localName: "getItem", methodKind: "unary", input: { fields: [{ name: "org_id" }] } },
  ],
};

const analysis = new OrgScopeAnalysis(root, { [join(root, "gen/fake_pb.ts")]: { FakeService: fakeService } });
const sites = analysis.sites();
const site = (kind: Site["kind"], fn: string) => sites.find((s) => s.kind === kind && s.fn === fn);

describe("OrgScopeAnalysis", () => {
  it("knows an rpc is org-scoped by its request's org_id", () => {
    expect([...analysis.orgScoped]).toEqual(["fake.v1.FakeService.listThings", "fake.v1.FakeService.getItem"]);
  });

  it("flags a generated read whose request names no org, and one that does not wait", () => {
    expect(site("generated", "useUnscoped")).toMatchObject({ namesOrg: false, gated: false });
    expect(site("generated", "useScoped")).toMatchObject({ namesOrg: true, gated: true, keepsAcrossOrgs: false });
  });

  it("ignores a read of an rpc that is not org-scoped, and a key derived without reading", () => {
    expect(site("generated", "useNotOrgScoped")).toBeUndefined();
    expect(site("generated", "keyOf")).toBeUndefined();
  });

  it("flags keepPreviousData; a cache-only read and an imperative read after the org are gated", () => {
    expect(site("generated", "useKeeps")?.keepsAcrossOrgs).toBe(true);
    expect(site("generated", "useCacheOnly")?.gated).toBe(true);
    expect(site("generated", "fetchThings")?.gated).toBe(true);
  });

  it("follows a hand-written queryFn through the functions it calls", () => {
    expect(site("handwritten", "useHandUnscoped")).toMatchObject({ rpcs: ["fake.v1.FakeService.listThings"], namesOrg: false });
    expect(site("handwritten", "useHandScoped")).toMatchObject({ namesOrg: true, fetchNamesOrg: true, gated: true });
    expect(site("handwritten", "useHandFetchesElsewhere")).toMatchObject({ namesOrg: true, fetchNamesOrg: false });
  });

  it("never takes a browser global's method for an rpc of the same name", () => {
    expect(site("handwritten", "useStorageOnly")?.rpcs ?? []).toEqual([]);
    expect(site("handwritten", "useStorageAlias")?.rpcs ?? []).toEqual([]);
    // An api object that wraps the client method by method still counts.
    expect(site("handwritten", "useWrapped")?.rpcs).toEqual(["fake.v1.FakeService.getItem"]);
    // So does one imported under a browser global's name.
    expect(site("handwritten", "useHistoryNamespace")?.rpcs).toEqual(["fake.v1.FakeService.getItem"]);
    expect(site("handwritten", "useHistoryPackage")?.rpcs).toEqual(["fake.v1.FakeService.getItem"]);
  });

  it("records a direct client call and whether its request names the org", () => {
    expect(site("call", "listThings")?.namesOrg).toBe(true);
    expect(site("call", "listAnyThings")?.namesOrg).toBe(false);
  });
});
