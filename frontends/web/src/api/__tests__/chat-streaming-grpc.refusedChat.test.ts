import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

// StreamChatUpdates refuses (NotFound) a chat that no longer exists or is not
// this user's. Every retry would carry the same chat and be refused the same
// way, so the chat stream drops the chat and goes idle instead of backing off
// against it forever. The user stream is a separate connection: a chat
// deleted from another device can no longer take down the app's push path.

type Request = { chatId: string; sinceSeq: bigint };

const chatRequests: Request[] = [];
const userRequests: unknown[] = [];
let respond: (request: Request) => AsyncIterable<unknown>;

// StreamingService is reached through connectClient; answer both streams here.
vi.mock("../../lib/connect", () => ({
  connectClient: () => ({
    streamChatUpdates: (request: Request) => {
      chatRequests.push({ chatId: request.chatId, sinceSeq: request.sinceSeq });
      return respond(request);
    },
    streamUserUpdates: (request: unknown) => {
      userRequests.push(request);
      return {
        async *[Symbol.asyncIterator]() {
          yield { event: { case: "heartbeat", value: { timestamp: 0n } } };
          await new Promise(() => undefined);
        },
      };
    },
  }),
}));

import { ChatStreamingService } from "../chat-streaming-grpc";
import { UserStreamingService } from "../streaming-grpc";

const GONE = "dddddddd-1111-2222-3333-444444444444";
const LIVE = "eeeeeeee-5555-6666-7777-888888888888";

const heartbeat = { event: { case: "heartbeat", value: { timestamp: 0n } } };

/** Refuse a stream for `refused`; otherwise send a heartbeat and stay open. */
function serverRefusing(refused: string) {
  return (request: Request): AsyncIterable<unknown> => ({
    async *[Symbol.asyncIterator]() {
      if (request.chatId === refused) {
        throw new ConnectError("chat not found", Code.NotFound);
      }
      yield heartbeat;
      await new Promise(() => undefined);
    },
  });
}

function makeService() {
  const onChatSubscriptionRejected = vi.fn<(chatId: string) => void>();
  const onChatSyncPending = vi.fn<(chatId: string | null) => void>();
  const onError = vi.fn<(error: string) => void>();
  const service = new ChatStreamingService({
    onChatUpdate: vi.fn(),
    onChatSnapshot: vi.fn(),
    onError,
    onChatSubscriptionRejected,
    onChatSyncPending,
  });
  return { service, onChatSubscriptionRejected, onChatSyncPending, onError };
}

afterEach(() => {
  chatRequests.length = 0;
  userRequests.length = 0;
});

describe("a chat the server refuses", () => {
  it("is dropped, and the stream goes idle instead of retrying it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    respond = serverRefusing(GONE);
    const { service, onChatSubscriptionRejected, onChatSyncPending, onError } = makeService();

    service.subscribeToChat(GONE);

    await vi.waitFor(() => expect(onChatSubscriptionRejected).toHaveBeenCalledWith(GONE));
    expect(service.getChatId()).toBeUndefined();
    expect(service.isConnected()).toBe(false);
    // The syncing overlay is not left up for a chat that will never sync.
    expect(onChatSyncPending).toHaveBeenLastCalledWith(null);
    // Not a stream fault: nothing to report.
    expect(onError).not.toHaveBeenCalled();
    // No retry is scheduled.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(chatRequests.map((r) => r.chatId)).toEqual([GONE]);
    service.stop();
    vi.useRealTimers();
  });

  it("will not be re-subscribed to by a later reconcile", async () => {
    respond = serverRefusing(GONE);
    const { service, onChatSubscriptionRejected } = makeService();
    service.subscribeToChat(GONE);
    await vi.waitFor(() => expect(onChatSubscriptionRejected).toHaveBeenCalled());

    service.subscribeToChat(GONE);

    expect(chatRequests).toHaveLength(1);
    expect(service.getChatId()).toBeUndefined();
    service.stop();
  });

  it("does not stop other chats from streaming", async () => {
    respond = serverRefusing(GONE);
    const { service, onChatSubscriptionRejected } = makeService();
    service.subscribeToChat(GONE);
    await vi.waitFor(() => expect(onChatSubscriptionRejected).toHaveBeenCalled());

    service.subscribeToChat(LIVE);

    await vi.waitFor(() => expect(service.isConnected()).toBe(true));
    expect(chatRequests.at(-1)?.chatId).toBe(LIVE);
    expect(service.getChatId()).toBe(LIVE);
    service.stop();
  });

  it("leaves the user stream connected", async () => {
    respond = serverRefusing(GONE);
    const userStream = new UserStreamingService({
      onUpdate: vi.fn(),
      onStatusChange: vi.fn(),
      onSync: vi.fn(),
      onError: vi.fn(),
    });
    userStream.start(0);
    await vi.waitFor(() => expect(userStream.isConnected()).toBe(true));
    const { service, onChatSubscriptionRejected } = makeService();

    service.subscribeToChat(GONE);
    await vi.waitFor(() => expect(onChatSubscriptionRejected).toHaveBeenCalled());

    expect(userStream.isConnected()).toBe(true);
    expect(userRequests).toHaveLength(1);
    service.stop();
    userStream.stop();
  });

  it("is an ordinary, retried failure once the stream had delivered", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let calls = 0;
    respond = () => ({
      async *[Symbol.asyncIterator]() {
        calls++;
        if (calls === 1) {
          yield heartbeat;
          throw new ConnectError("not found", Code.NotFound);
        }
        yield heartbeat;
        await new Promise(() => undefined);
      },
    });
    const { service, onChatSubscriptionRejected, onError } = makeService();

    service.subscribeToChat(LIVE);

    await vi.waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onChatSubscriptionRejected).not.toHaveBeenCalled();
    // Backing off, not hammering: the retry is on a timer, and keeps the chat.
    expect(chatRequests).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(3_000);
    await vi.waitFor(() => expect(chatRequests).toHaveLength(2));
    expect(chatRequests[1].chatId).toBe(LIVE);
    service.stop();
    vi.useRealTimers();
  });
});
