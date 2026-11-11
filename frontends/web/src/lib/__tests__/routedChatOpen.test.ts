import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { beginChatOpen, resetChatOpen } from "../chatOpenInteraction";
import { watchRoutedChatOpens, type ChatOpenRouter } from "../routedChatOpen";
import { clearAllMessagesCache, setMessagesInCache } from "../../hooks/message-queries";
import { installRecordingTracer } from "../../test/recordingTracer";
import type { Message } from "../../api/client";

// The mobile chat screen opens its chat from the URL, never through
// chatStore.selectChat: the router's navigations start its chat.open.

type Listener = Parameters<ChatOpenRouter["subscribe"]>[1];

let tracer: ReturnType<typeof installRecordingTracer>;
const opens = () => tracer.spans.filter((s) => s.name === "chat.open");

/** A router that navigates on command, and the paths the chat screen owns. */
function fakeRouter() {
  let listener: Listener | undefined;
  const router: ChatOpenRouter = {
    subscribe: (_event, fn) => {
      listener = fn;
      return () => {
        listener = undefined;
      };
    },
  };
  let resolved: string | undefined;
  const navigate = (pathname: string) => {
    listener?.({
      fromLocation: resolved === undefined ? undefined : { pathname: resolved },
      toLocation: { pathname },
    });
    resolved = pathname;
  };
  const chatIdAt = (pathname: string) => /^\/m\/chats\/([^/]+)$/.exec(pathname)?.[1];
  const stop = watchRoutedChatOpens(router, chatIdAt);
  return { navigate, stop };
}

beforeAll(() => {
  tracer = installRecordingTracer();
});
afterAll(() => tracer.uninstall());

beforeEach(() => {
  clearAllMessagesCache();
});
afterEach(() => {
  resetChatOpen();
  tracer.spans.length = 0;
});

describe("watchRoutedChatOpens", () => {
  it("times a chat the app loaded on as a deep link", () => {
    const { navigate } = fakeRouter();

    navigate("/m/chats/c1");

    expect(opens()).toHaveLength(1);
    expect(opens()[0]!.attributes).toMatchObject({
      "chat.id": "c1",
      "chat.open.entry": "deep_link",
      "chat.cached": false,
      "chat.cached_messages": 0,
    });
  });

  it("times a chat opened from the list as a mobile route, saying whether it was cached", () => {
    setMessagesInCache("c1", [{ id: "m-1" }] as Message[]);
    const { navigate } = fakeRouter();
    navigate("/m/chats");

    navigate("/m/chats/c1");

    expect(opens()).toHaveLength(1);
    expect(opens()[0]!.attributes).toMatchObject({
      "chat.id": "c1",
      "chat.open.entry": "mobile_route",
      "chat.cached": true,
      "chat.cached_messages": 1,
    });
  });

  it("supersedes the open in flight when the next chat is opened beside the list", () => {
    const { navigate } = fakeRouter();
    navigate("/m/chats/c1");

    navigate("/m/chats/c2");

    const [first, second] = opens();
    expect(first!.attributes["interaction.outcome"]).toBe("superseded");
    expect(second!.attributes).toMatchObject({ "chat.id": "c2", "chat.open.entry": "mobile_route" });
    expect(second!.ended).toBe(false);
  });

  it("does not start a second open for a new chat selectChat already opened", () => {
    const { navigate } = fakeRouter();
    navigate("/m/new");
    beginChatOpen("c-new");

    navigate("/m/chats/c-new");

    expect(opens()).toHaveLength(1);
    expect(opens()[0]!.attributes["chat.open.entry"]).toBe("switch");
  });

  it("starts nothing for screens that are not a chat", () => {
    const { navigate } = fakeRouter();

    navigate("/m/chats");
    navigate("/m/inbox");
    navigate("/m/chats/c1/workflow");

    expect(opens()).toHaveLength(0);
  });

  it("stops when unsubscribed", () => {
    const { navigate, stop } = fakeRouter();
    stop();

    navigate("/m/chats/c1");

    expect(opens()).toHaveLength(0);
  });
});
