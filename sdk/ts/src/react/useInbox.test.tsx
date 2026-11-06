// @vitest-environment jsdom
import { act, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { UserUpdateType } from "#gen/reliant/v1/streaming_pb.js";
import { harness, settle } from "../test/react-harness.js";
import { inboxInvalidatingUpdate, useInbox } from "./useInbox.js";

const item = (itemId: string) => ({ itemId, chatId: "c1", kind: 1 });

function inboxServer() {
  let items = [item("i1"), item("i2"), item("i3")];
  const state = { reads: 0, requests: [] as Array<Record<string, unknown>>, dismissed: [] as string[][] };
  return {
    state,
    set: (next: string[]) => (items = next.map(item)),
    handlers: {
      ListInbox: (r: Record<string, unknown>) => {
        state.reads++;
        state.requests.push(r);
        return { items, blockingCount: items.length, hasInformational: true, truncated: false };
      },
      DismissInboxItem: (r: Record<string, unknown>) => {
        state.dismissed.push(r.itemIds as string[]);
        items = items.filter((i) => !(r.itemIds as string[]).includes(i.itemId));
        return {};
      },
      RestoreInboxItem: () => ({}),
    },
  };
}

describe("useInbox", () => {
  it("lists the items with the counts, scoped to a project", async () => {
    const s = inboxServer();
    const h = harness(s.handlers);
    const { result } = h.render(() => useInbox({ projectId: "p1", live: false }));
    await waitFor(() => expect(result.current.items).toHaveLength(3));
    expect(result.current.counts).toEqual({ blockingCount: 3, hasInformational: true });
    expect(result.current.truncated).toBe(false);
    expect(s.state.requests[0]).toMatchObject({ projectId: "p1" });
  });

  it("dismissing removes the items at once, and puts them back if the write fails", async () => {
    let fail = false;
    const s = inboxServer();
    const h = harness({
      ...s.handlers,
      DismissInboxItem: (r) => {
        if (fail) throw new ConnectError("no", Code.Internal);
        return s.handlers.DismissInboxItem(r);
      },
    });
    const { result } = h.render(() => useInbox({ live: false }));
    await waitFor(() => expect(result.current.items).toHaveLength(3));

    await act(() => result.current.dismiss(["i1"]));
    await waitFor(() => expect(result.current.items.map((i) => i.itemId)).toEqual(["i2", "i3"]));
    expect(s.state.dismissed).toEqual([["i1"]]);

    fail = true;
    await act(async () => {
      await expect(result.current.dismiss(["i2"])).rejects.toThrow("no");
    });
    await waitFor(() => expect(result.current.items.map((i) => i.itemId)).toEqual(["i2", "i3"]));
  });

  it("remove() takes items out of the cached list now and reads the inbox again", async () => {
    const s = inboxServer();
    const h = harness(s.handlers);
    const { result } = h.render(() => useInbox({ live: false }));
    await waitFor(() => expect(result.current.items).toHaveLength(3));

    s.set(["i3"]);
    act(() => result.current.remove(["i1", "i2"]));
    await waitFor(() => expect(result.current.items.map((i) => i.itemId)).toEqual(["i3"]));
    await waitFor(() => expect(s.state.reads).toBe(2));
  });

  it("reads the inbox again when the user stream reports a change, and only then", async () => {
    const s = inboxServer();
    const h = harness(s.handlers);
    const { result, unmount } = h.render(() => useInbox());
    await waitFor(() => expect(result.current.items).toHaveLength(3));
    await settle();

    const stream = h.server.connections.find((c) => c.method === "StreamUserUpdates");
    expect(stream).toBeDefined();
    const update = (updateType: UserUpdateType, dataJson = "{}") => ({
      id: "u", userId: "me", sequenceNumber: 1n, updateType, entityId: "e", dataJson, createdAt: "",
    });
    const updates = (...u: ReturnType<typeof update>[]) => ({ event: { case: "updates", value: { updates: u, latestSequence: 2n } } });

    // An unrelated change leaves the read alone ...
    act(() => stream!.push(updates(update(UserUpdateType.REFETCH, '{"type":"plan_tasks"}')) as never));
    await settle();
    expect(s.state.reads).toBe(1);

    // ... a chat activity change reads again ...
    s.set(["i2"]);
    act(() => stream!.push(updates(update(UserUpdateType.CHAT_ACTIVITY_CHANGED)) as never));
    await waitFor(() => expect(result.current.items.map((i) => i.itemId)).toEqual(["i2"]));
    expect(s.state.reads).toBe(2);

    // ... and so does the inbox refetch signal of an async question.
    s.set([]);
    act(() => stream!.push(updates(update(UserUpdateType.REFETCH, '{"type":"inbox"}')) as never));
    await waitFor(() => expect(result.current.items).toEqual([]));
    unmount();
    await settle();
    expect(stream!.signal?.aborted).toBe(true);
  });

  it("holds no stream when it is not live", async () => {
    const s = inboxServer();
    const h = harness(s.handlers);
    h.render(() => useInbox({ live: false }));
    await settle();
    expect(h.server.connections).toHaveLength(0);
  });

  it("names the updates that can change the inbox", () => {
    const u = (update_type: UserUpdateType, data: Record<string, unknown> = {}) => ({ update_type, data });
    expect(inboxInvalidatingUpdate(u(UserUpdateType.CHAT_ACTIVITY_CHANGED))).toBe(true);
    expect(inboxInvalidatingUpdate(u(UserUpdateType.CHAT_STATE_CHANGE))).toBe(true);
    expect(inboxInvalidatingUpdate(u(UserUpdateType.REFETCH, { type: "inbox" }))).toBe(true);
    expect(inboxInvalidatingUpdate(u(UserUpdateType.REFETCH, { type: "runs" }))).toBe(false);
  });
});
