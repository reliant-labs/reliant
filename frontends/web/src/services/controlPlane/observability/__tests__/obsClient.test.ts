import { describe, expect, it, vi } from "vitest";

import { runAsPoll } from "../quotaStore";

const calls: { request: unknown; options: unknown }[] = [];
const fake = {
  queryPanel: vi.fn(async (request: unknown, options: unknown) => {
    calls.push({ request, options });
    return {};
  }),
};
vi.mock("../../client", () => ({ getControlPlaneClient: () => fake }));

import { obsClient } from "../obsClient";

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
});
