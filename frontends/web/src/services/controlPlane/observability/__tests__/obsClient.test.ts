import { describe, expect, it, vi } from "vitest";

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";

import { runAsPoll } from "../quotaStore";

const calls: { request: unknown; options: unknown }[] = [];
const fake = {
  queryPanel: vi.fn(async (request: unknown, options: unknown) => {
    calls.push({ request, options });
    return {};
  }),
};
vi.mock("../../client", () => ({ getControlPlaneClient: () => fake }));

import { NO_READ_SLOT_RPCS, obsClient, READ_SLOT_RPCS } from "../obsClient";

describe("obsClient", () => {
  it("marks a background poll with Forge-Poll and a person's read without it", async () => {
    calls.length = 0;
    const client = obsClient({} as never) as unknown as { queryPanel: (r: unknown) => Promise<unknown> };

    await client.queryPanel({ id: "person" });
    runAsPoll(() => void client.queryPanel({ id: "poll" }));
    await client.queryPanel({ id: "person again" });

    expect(calls.map((c) => (c.options as { headers?: Record<string, string> } | undefined)?.headers?.["Forge-Poll"])).toEqual([undefined, "1", undefined]);
  });

  it("keeps the headers a caller passes", async () => {
    calls.length = 0;
    const client = obsClient({} as never) as unknown as { queryPanel: (r: unknown, o?: unknown) => Promise<unknown> };
    runAsPoll(() => void client.queryPanel({}, { headers: { "X-Other": "1" } }));
    expect((calls[0]?.options as { headers: Record<string, string> }).headers).toEqual({ "X-Other": "1", "Forge-Poll": "1" });
  });

  it("keeps the poll mark when the scheduler starts the call after the poll that asked for it", async () => {
    calls.length = 0;
    const client = obsClient({} as never) as unknown as { queryPanel: (r: unknown) => Promise<unknown> };
    runAsPoll(() => void client.queryPanel({ id: "queued poll" }));
    await Promise.resolve();
    await Promise.resolve();
    expect((calls[0]?.options as { headers: Record<string, string> }).headers["Forge-Poll"]).toBe("1");
  });
});

describe("which RPCs wait in the read queue", () => {
  const methods = [ObservabilityService, IssuesService, RumService].flatMap((service) => service.methods.map((method) => method.localName));

  it("classifies every method of the three services: a new RPC must say whether it takes a server read slot", () => {
    const unclassified = methods.filter((name) => !READ_SLOT_RPCS.has(name) && !NO_READ_SLOT_RPCS.has(name));
    expect(unclassified).toEqual([]);
  });

  it("names only methods that exist", () => {
    const known = new Set(methods);
    expect([...READ_SLOT_RPCS, ...NO_READ_SLOT_RPCS].filter((name) => !known.has(name))).toEqual([]);
  });

  it("never queues a mutation behind the reads", () => {
    for (const name of ["updateIssueStatus", "addIssueComment", "updateIssueComment", "deleteIssueComment", "assignIssue", "linkIssueChat"]) {
      expect(READ_SLOT_RPCS.has(name)).toBe(false);
    }
  });
});
