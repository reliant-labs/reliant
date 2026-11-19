// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * EVERY READ OF AN ORG-SCOPED RPC NAMES ITS ORG AND WAITS FOR IT.
 *
 * A request that names no org is answered for whichever org the server picks
 * (the transport's orgInterceptor fills the active org; some rpcs read unset as
 * "every org"). A cache key that does not name the org then serves one org's
 * answer as another's, after a switch or on a first load (reliant#2837, the
 * unified-shell design's U2). So, for every query in src/ on an org-scoped rpc:
 *
 *   1. its key names the org (a generated hook's key is its request, so the
 *      request names it: `orgRequest(req, org.orgId)`), and a hand-written
 *      query's fetch names the same org, so the request sent is the one the
 *      key describes;
 *   2. it waits for the org: `enabled` says so (`orgQueryEnabled(org)`,
 *      `!!orgId`), or it is an imperative read that awaits `whenOrgScopeReady`;
 *   3. it never keeps the previous answer across orgs: `placeholderData` is
 *      `keepPreviousInOrg(…)`/`keepPreviousInOrgSegment(…)`, never
 *      `keepPreviousData`.
 *
 * "Org-scoped" is derived, never listed: the rpc's request message has an
 * `org_id` field in the generated `_pb.ts`. A new rpc, or a new read of an old
 * one, is covered the day it lands. The analysis is static (test/orgScopeAnalysis.ts);
 * a deliberate exception goes in ALLOWED with the reason it is safe.
 */

import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { OrgScopeAnalysis, type Site } from "@/test/orgScopeAnalysis";

const SRC = join(__dirname, "..");

const pbModules: Record<string, Record<string, unknown>> = {};
for (const [path, mod] of Object.entries(import.meta.glob("../gen/**/*_pb.ts", { eager: true }))) {
  pbModules[join(__dirname, path)] = mod as Record<string, unknown>;
}

/** Deliberate exceptions, by `<file under src/>#<function>`, each with why it is safe. */
const ALLOWED: Record<string, string> = {
  "hooks/forge-queries.ts#liveViewQuery":
    "A builder, not a read: useLiveView adds `enabled` on the tab's org scope, and QueuedDeployNotifier reads in the org a watched reading was taken in.",
};

const analysis = new OrgScopeAnalysis(SRC, pbModules);
const sites = analysis.sites();
const reads = sites.filter((site) => site.kind !== "call");

const idOf = (site: Site) => `${site.where.replace(/:\d+$/, "")}#${site.fn}`;
const allowed = (site: Site) => idOf(site) in ALLOWED;
const describeSite = (site: Site) => `  ${site.where} (${site.fn || "top level"}, ${site.kind}): ${site.rpcs.join(", ")}`;

function report(offenders: Site[], fix: string): string {
  return `${offenders.length} read(s) of an org-scoped rpc ${fix}\n${offenders.map(describeSite).join("\n")}`;
}

describe("org-scoped queries", () => {
  it("derives org-scoped rpcs from the request messages' org_id field", () => {
    expect(analysis.orgScoped.has("reliant.v1.ProjectService.listProjects")).toBe(true);
    expect(analysis.orgScoped.has("controlplane.v1.OrganizationService.listMembers")).toBe(true);
    // GetProject's request names a project, not an org.
    expect(analysis.orgScoped.has("reliant.v1.ProjectService.getProject")).toBe(false);
  });

  it("finds the reads it guards", () => {
    // A guard that silently stopped seeing reads would pass forever: pin a few
    // of each kind, and the rough size of the whole.
    const fns = new Set(reads.map((site) => `${site.kind}:${site.fn}`));
    expect(fns).toContain("generated:useProjects");
    expect(fns).toContain("generated:useTriggers");
    expect(fns).toContain("handwritten:useMembers");
    expect(fns).toContain("handwritten:useForgeDomains");
    expect(reads.length).toBeGreaterThan(60);
  });

  it("every read names the org in its key", () => {
    const offenders = reads.filter((site) => !site.namesOrg && !allowed(site));
    expect(offenders, report(offenders, "with no org in its key (name it: orgRequest(req, org.orgId), or an org segment)")).toEqual([]);
  });

  it("every hand-written read fetches in the org its key names", () => {
    const offenders = reads.filter((site) => !site.fetchNamesOrg && !allowed(site));
    expect(offenders, report(offenders, "whose queryFn does not name the org its key does (pass it to the request)")).toEqual([]);
  });

  it("every read waits for the org", () => {
    const offenders = reads.filter((site) => !site.gated && !allowed(site));
    expect(offenders, report(offenders, "with no org gate (enabled: orgQueryEnabled(org) && …, or await whenOrgScopeReady())")).toEqual([]);
  });

  it("no read keeps another org's answer on screen", () => {
    const offenders = reads.filter((site) => site.keepsAcrossOrgs);
    expect(offenders, report(offenders, "with placeholderData: keepPreviousData (use keepPreviousInOrg(org.orgId))")).toEqual([]);
  });

  it("every allowlist entry still names a read", () => {
    const ids = new Set(reads.map(idOf));
    expect(Object.keys(ALLOWED).filter((id) => !ids.has(id))).toEqual([]);
  });
});
