// Copyright (c) 2025 Reliant Labs

/**
 * The setting `provider.org_funding` as the web reads and writes it: the same
 * reading as the server (internal/orgfunding), so a bad value can only ask or
 * wait, never spend the person's money.
 */

import { describe, expect, it } from "vitest";

import {
  DEFAULT_ORG_FUNDING,
  isDefaultOrgFunding,
  orgFundingFor,
  parseOrgFunding,
  resetOrgFunding,
  withOrgFunding,
} from "../orgFunding";

describe("orgFunding", () => {
  it("reads a missing, empty or unreadable value as the defaults: org first, ask", () => {
    for (const v of [undefined, "", "  ", "not json", "[]", "null", "42"]) {
      expect(orgFundingFor(parseOrgFunding(v), "acme")).toEqual(DEFAULT_ORG_FUNDING);
    }
  });

  it("reads each org's entry, filling an unknown or missing field with its default", () => {
    const s = parseOrgFunding(
      JSON.stringify({
        acme: { order: "personal_first", on_org_limit: "use_personal" },
        beta: { order: "org_only" },
        gamma: { order: "spend_everything", on_org_limit: "always" },
      }),
    );
    expect(orgFundingFor(s, "acme")).toEqual({ order: "personal_first", onOrgLimit: "use_personal" });
    expect(orgFundingFor(s, "beta")).toEqual({ order: "org_only", onOrgLimit: "ask" });
    expect(orgFundingFor(s, "gamma")).toEqual(DEFAULT_ORG_FUNDING);
    expect(orgFundingFor(s, "nobody")).toEqual(DEFAULT_ORG_FUNDING);
  });

  it("writes one org's entry in the server's shape, keeping the other field", () => {
    const s = parseOrgFunding(JSON.stringify({ acme: { order: "personal_first" } }));
    const next = withOrgFunding(s, "acme", { onOrgLimit: "wait" });
    expect(next).toEqual({ acme: { order: "personal_first", on_org_limit: "wait" } });
    expect(JSON.parse(JSON.stringify(next))).toEqual({ acme: { order: "personal_first", on_org_limit: "wait" } });
  });

  it("drops the entries of orgs the person left on a write, and keeps the one written", () => {
    const s = parseOrgFunding(JSON.stringify({ acme: { order: "org_only" }, left: { order: "personal_first" } }));
    const keep = (id: string) => id === "acme";
    expect(Object.keys(withOrgFunding(s, "acme", { onOrgLimit: "wait" }, keep))).toEqual(["acme"]);
    expect(Object.keys(withOrgFunding(s, "new", { order: "org_first" }, keep)).sort()).toEqual(["acme", "new"]);
  });

  it("resets an org to the defaults by removing its entry", () => {
    const s = parseOrgFunding(JSON.stringify({ acme: { on_org_limit: "use_personal" }, beta: { order: "org_only" } }));
    expect(isDefaultOrgFunding(s, "acme")).toBe(false);
    const next = resetOrgFunding(s, "acme");
    expect(next).toEqual({ beta: { order: "org_only" } });
    expect(isDefaultOrgFunding(next, "acme")).toBe(true);
  });
});
