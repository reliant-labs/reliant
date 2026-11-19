// @vitest-environment node
//
// The web pages a transcript by cursor (oldestSeq, hasMore) and reads no
// total, so every ListMessages it makes asks the server to skip the count:
// counting a chat of a million messages took 9.9s, longer than the read
// (obs 16122d5e).

import { afterEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { listMessages } from "../chat-actions";

afterEach(() => setTransport(null));

describe("ListMessages skip_total", () => {
  it("is set on every read the web makes", async () => {
    const listMessagesRpc = vi.fn(async () => ({ messages: [], hasMore: false, oldestSeq: 0n, errors: [] }));
    setTransport(mockTransport({ "ChatService.ListMessages": listMessagesRpc }));

    await listMessages("chat-1", { recent: 50 });
    await listMessages("chat-1", { recent: 50, beforeSeq: 10 });
    await listMessages("chat-1", { threadId: "thread-1" });
    // ChatSearch's current-chat search: the unbounded read.
    await listMessages("chat-1");

    expect(listMessagesRpc).toHaveBeenCalledTimes(4);
    for (const [request] of listMessagesRpc.mock.calls as unknown as Array<[{ skipTotal?: boolean }]>) {
      expect(request.skipTotal).toBe(true);
    }
  });
});
