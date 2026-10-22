import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

// StreamUserUpdates refuses the WHOLE stream (NotFound) when its subscribed
// chat no longer exists or is not this user's. Every retry carried the same
// chat and was refused the same way, so a chat deleted from another device —
// or one left subscribed after it was deleted here — took down the app's only
// push path until a reload: no chat list updates, no activity, no daemon
// heartbeats, just a reconnect backoff climbing to its cap.

type Request = { subscribeChatId?: string };

const requests: Request[] = [];
let respond: (request: Request) => AsyncIterable<unknown>;

vi.mock("@connectrpc/connect", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@connectrpc/connect")>();
  return {
    ...actual,
    createClient: () => ({
      streamUserUpdates: (request: Request) => {
        requests.push({ subscribeChatId: request.subscribeChatId });
        return respond(request);
      },
    }),
  };
});

vi.mock("../grpc-client", () => ({ getStreamingTransport: () => ({}) }));

import { UserStreamingService } from "../streaming-grpc";

const GONE = "dddddddd-1111-2222-3333-444444444444";
const LIVE = "eeeeeeee-5555-6666-7777-888888888888";

const heartbeat = { event: { case: "heartbeat", value: { timestamp: 0n } } };

/** Refuse any stream subscribed to `refused`; otherwise send a heartbeat and stay open. */
function serverRefusing(refused: string) {
  return (request: Request): AsyncIterable<unknown> => ({
    async *[Symbol.asyncIterator]() {
      if (request.subscribeChatId === refused) {
        throw new ConnectError("chat not found", Code.NotFound);
      }
      yield heartbeat;
      await new Promise(() => undefined);
    },
  });
}

function makeService() {
  const onChatSubscriptionRejected = vi.fn<(chatId: string) => void>();
  const onError = vi.fn<(error: string) => void>();
  const onStatusChange = vi.fn();
  const service = new UserStreamingService({
    onUpdate: vi.fn(),
    onStatusChange,
    onSync: vi.fn(),
    onError,
    onChatUpdate: vi.fn(),
    onChatSubscriptionRejected,
  });
  return { service, onChatSubscriptionRejected, onError, onStatusChange };
}

afterEach(() => {
  requests.length = 0;
});

describe("a stream refused because of its chat", () => {
  it("drops the chat and reconnects at once without it", async () => {
    respond = serverRefusing(GONE);
    const { service, onChatSubscriptionRejected, onError } = makeService();

    service.start(0, GONE);

    await vi.waitFor(() => expect(service.isConnected()).toBe(true));
    expect(requests.map((r) => r.subscribeChatId)).toEqual([GONE, undefined]);
    expect(service.getSubscribedChatId()).toBeUndefined();
    expect(onChatSubscriptionRejected).toHaveBeenCalledWith(GONE);
    // Not a stream fault: nothing to report, and no disruption to recover from.
    expect(onError).not.toHaveBeenCalled();
    service.stop();
  });

  it("will not be re-subscribed to that chat by a later reconcile", async () => {
    respond = serverRefusing(GONE);
    const { service } = makeService();
    service.start(0, GONE);
    await vi.waitFor(() => expect(service.isConnected()).toBe(true));
    const before = requests.length;

    service.subscribeToChatDetails(GONE);

    expect(requests.length).toBe(before);
    expect(service.getSubscribedChatId()).toBeUndefined();
    service.stop();
  });

  it("still subscribes to other chats", async () => {
    respond = serverRefusing(GONE);
    const { service } = makeService();
    service.start(0, GONE);
    await vi.waitFor(() => expect(service.isConnected()).toBe(true));

    service.subscribeToChatDetails(LIVE);

    await vi.waitFor(() =>
      expect(requests.at(-1)?.subscribeChatId).toBe(LIVE),
    );
    expect(service.getSubscribedChatId()).toBe(LIVE);
    service.stop();
  });

  it("is treated as an ordinary failure when no chat is subscribed", async () => {
    respond = () => ({
      [Symbol.asyncIterator]: () => ({
        next: () => Promise.reject(new ConnectError("not found", Code.NotFound)),
      }),
    });
    const { service, onChatSubscriptionRejected, onError } = makeService();

    service.start(0);

    await vi.waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onChatSubscriptionRejected).not.toHaveBeenCalled();
    // Backing off, not hammering: one attempt, the retry is on a timer.
    expect(requests).toHaveLength(1);
    service.stop();
  });
});
