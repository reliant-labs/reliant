// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";

import { isDemoRead } from "../readOnly";
import { createFixtureTransport, defineFixturePack, type FixtureContext } from "../transport";

const GET_ISSUE = `${IssuesService.typeName}/GetIssue`;
const MESSAGE = "Changes are turned off.";
const FIXED_NOW = Date.parse("2026-11-18T15:30:00Z");

describe("createFixtureTransport", () => {
  it("serves a pack's scenario, built with the pack context, over the descriptor-derived defaults", async () => {
    let seen: FixtureContext | undefined;
    const pack = defineFixturePack({
      name: "t",
      idPrefix: "t.",
      scenario: (ctx) => ((seen = ctx), { [GET_ISSUE]: () => ({ issue: { id: ctx.id("one") } }) }),
      defaults: [IssuesService],
    });
    const client = createClient(IssuesService, createFixtureTransport({ pack, now: () => FIXED_NOW }));
    expect((await client.getIssue({})).issue?.id).toBe("t.one");
    expect(seen?.now()).toBe(FIXED_NOW);
    expect(Array.isArray((await client.listIssues({})).issues)).toBe(true);
  });

  it("lets the options' own scenario win over the pack's", async () => {
    const pack = defineFixturePack({ name: "t", scenario: { [GET_ISSUE]: () => ({ issue: { id: "pack" } }) } });
    const client = createClient(IssuesService, createFixtureTransport({ pack, scenario: { name: "top", handlers: { [GET_ISSUE]: () => ({ issue: { id: "top" } }) } } }));
    expect((await client.getIssue({})).issue?.id).toBe("top");
  });

  it("refuses every write before a handler sees it, with the given message", async () => {
    const client = createClient(IssuesService, createFixtureTransport({ defaults: [IssuesService], readOnly: isDemoRead, readOnlyMessage: MESSAGE }));
    await expect(client.listIssues({})).resolves.toBeDefined();
    await expect(client.assignIssue({})).rejects.toSatisfy((e) => e instanceof ConnectError && e.code === Code.PermissionDenied && e.rawMessage === MESSAGE);
  });

  it("refuses what it does not support yet, rather than ignoring it", () => {
    expect(() => createFixtureTransport({ routes: () => {} })).toThrow(/routes not supported/);
    expect(() => defineFixturePack({ name: "" })).toThrow(/non-empty name/);
  });
});
