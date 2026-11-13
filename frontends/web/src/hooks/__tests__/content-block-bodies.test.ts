// @vitest-environment node
//
// The body batcher behind hooks/content-block-bodies.ts: ids asked for before
// a flush share one request, a request names at most 100 ids, ids the server
// defers are asked for again, and an id it cannot find is remembered so a
// remount does not ask again.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ContentBlockBodiesClient } from "@reliantlabs/sdk/client";
import {
  ContentBlockBodyNotFoundError,
  cachedContentBlockBody,
  contentBlockBodyQueryKey,
  createContentBlockBodyBatcher,
  elidedBlockIdsOf,
  fetchContentBlockBodies,
} from "../content-block-bodies";
import { queryClient } from "../../lib/query-client";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";

type Request = { chatId: string; blockIds: string[]; full?: boolean };

/** A server that answers every id it is asked for, deferring the ids `defer` names on their first ask. */
function fakeServer({ missing = [], defer = [] }: { missing?: string[]; defer?: string[] } = {}) {
  const requests: Request[] = [];
  const deferredOnce = new Set(defer);
  const client: ContentBlockBodiesClient = {
    getContentBlockBodies: vi.fn(async (request: Request) => {
      requests.push({ ...request, blockIds: [...request.blockIds] });
      const deferred = request.blockIds.filter((id) => deferredOnce.delete(id));
      const answered = request.blockIds.filter((id) => !deferred.includes(id));
      return {
        bodies: answered
          .filter((id) => !missing.includes(id))
          .map((id) => ({ blockId: id, input: `{"id":"${id}"}` })),
        notFound: answered.filter((id) => missing.includes(id)),
        deferred,
      };
    }),
  };
  return { client, requests };
}

/** A scheduler the test flushes by hand. */
function manualSchedule() {
  const queued: Array<() => void> = [];
  return {
    schedule: (flush: () => void) => queued.push(flush),
    flush: () => queued.splice(0).forEach((fn) => fn()),
  };
}

describe("the content block body batcher", () => {
  it("sends the ids asked for before one flush as one request", async () => {
    const { client, requests } = fakeServer();
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const loads = ["b1", "b2", "b1", "b3"].map((id) => batcher.load("chat-1", id, false));
    expect(requests).toHaveLength(0);
    timing.flush();
    const bodies = await Promise.all(loads);

    expect(requests).toEqual([{ chatId: "chat-1", blockIds: ["b1", "b2", "b3"], full: undefined }]);
    expect(bodies.map((b) => b.body.blockId)).toEqual(["b1", "b2", "b1", "b3"]);
  });

  it("keeps display and full reads, and different chats, in separate requests", async () => {
    const { client, requests } = fakeServer();
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const loads = [
      batcher.load("chat-1", "b1", false),
      batcher.load("chat-1", "b2", true),
      batcher.load("chat-2", "b3", false),
    ];
    timing.flush();
    await Promise.all(loads);

    expect(requests).toEqual([
      { chatId: "chat-1", blockIds: ["b1"], full: undefined },
      { chatId: "chat-1", blockIds: ["b2"], full: true },
      { chatId: "chat-2", blockIds: ["b3"], full: undefined },
    ]);
  });

  it("names at most 100 ids a request", async () => {
    const { client, requests } = fakeServer();
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const ids = Array.from({ length: 250 }, (_, i) => `b${i}`);
    const loads = ids.map((id) => batcher.load("chat-1", id, false));
    timing.flush();
    await Promise.all(loads);

    expect(requests.map((r) => r.blockIds.length)).toEqual([100, 100, 50]);
    expect(new Set(requests.flatMap((r) => r.blockIds)).size).toBe(250);
  });

  it("asks again for what the server deferred, and says it was deferred", async () => {
    const { client, requests } = fakeServer({ defer: ["b2"] });
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const loads = ["b1", "b2"].map((id) => batcher.load("chat-1", id, false));
    timing.flush();
    const [first, second] = await Promise.all(loads);

    expect(requests.map((r) => r.blockIds)).toEqual([["b1", "b2"], ["b2"]]);
    expect(first).toMatchObject({ body: { blockId: "b1" }, deferred: false });
    expect(second).toMatchObject({ body: { blockId: "b2" }, deferred: true });
  });

  it("remembers an id the server did not find, until a Retry forgets it", async () => {
    const { client, requests } = fakeServer({ missing: ["gone"] });
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const first = batcher.load("chat-1", "gone", false);
    timing.flush();
    await expect(first).rejects.toBeInstanceOf(ContentBlockBodyNotFoundError);

    // A remount asks again: answered from memory, no request.
    await expect(batcher.load("chat-1", "gone", false)).rejects.toBeInstanceOf(ContentBlockBodyNotFoundError);
    expect(requests).toHaveLength(1);

    batcher.forgetNotFound("chat-1", "gone");
    const retried = batcher.load("chat-1", "gone", false);
    timing.flush();
    await expect(retried).rejects.toBeInstanceOf(ContentBlockBodyNotFoundError);
    expect(requests).toHaveLength(2);
  });

  it("fails every id of a request the server could not answer (an older server: Unimplemented)", async () => {
    const client: ContentBlockBodiesClient = {
      getContentBlockBodies: vi.fn(async () => {
        throw new Error("unimplemented");
      }),
    };
    const timing = manualSchedule();
    const batcher = createContentBlockBodyBatcher(() => client, timing.schedule);

    const loads = ["b1", "b2"].map((id) => batcher.load("chat-1", id, false));
    timing.flush();
    for (const load of loads) await expect(load).rejects.toThrow("unimplemented");
    // Not remembered as not-found: the next ask goes to the server.
    const again = batcher.load("chat-1", "b1", false);
    timing.flush();
    await expect(again).rejects.toThrow("unimplemented");
    expect(client.getContentBlockBodies).toHaveBeenCalledTimes(2);
  });
});

describe("fetchContentBlockBodies (copy and export)", () => {
  const getContentBlockBodies = vi.fn();
  beforeEach(() => {
    queryClient.clear();
    getContentBlockBodies.mockReset();
    getContentBlockBodies.mockImplementation(async (request: Request) => ({
      bodies: request.blockIds.filter((id) => id !== "gone").map((id) => ({ blockId: id, content: `whole ${id}` })),
      notFound: request.blockIds.filter((id) => id === "gone"),
      deferred: [],
    }));
    setTransport(mockTransport({ "ChatService.GetContentBlockBodies": (req: unknown) => getContentBlockBodies(req) }));
  });
  afterEach(() => {
    setTransport(null);
    queryClient.clear();
  });

  it("reads every id in one full request and reports the ones not found", async () => {
    const result = await fetchContentBlockBodies("chat-1", ["r1", "r2", "gone", "r1"], { full: true });

    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ chatId: "chat-1", blockIds: ["r1", "r2", "gone"], full: true });
    expect(Object.keys(result.bodies).sort()).toEqual(["r1", "r2"]);
    expect(result.notFound).toEqual(["gone"]);
  });

  it("takes a display body already held when it is whole, and asks for a truncated one", async () => {
    queryClient.setQueryData(contentBlockBodyQueryKey("chat-1", "in", false), {
      body: { blockId: "in", input: "{}" },
      deferred: false,
    });
    queryClient.setQueryData(contentBlockBodyQueryKey("chat-1", "big", false), {
      body: { blockId: "big", content: "head … tail", truncated: true },
      deferred: false,
    });

    const result = await fetchContentBlockBodies("chat-1", ["in", "big"], { full: true });

    expect(result.bodies.in.input).toBe("{}");
    expect(result.bodies.big.content).toBe("whole big");
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ blockIds: ["big"], full: true });
    expect(cachedContentBlockBody("chat-1", "big", true)?.content).toBe("whole big");
  });
});

describe("elidedBlockIdsOf", () => {
  it("lists the elided inputs and results, and nothing sent whole", () => {
    expect(
      elidedBlockIdsOf([
        { call: { inputElided: true, content_block_id: "call-1" }, result: { elided: true, block_id: "res-1" } },
        { call: { content_block_id: "call-2" }, result: { block_id: "res-2" } },
        { call: { inputElided: true, content_block_id: "call-3" } },
      ]),
    ).toEqual(["call-1", "res-1", "call-3"]);
  });
});
