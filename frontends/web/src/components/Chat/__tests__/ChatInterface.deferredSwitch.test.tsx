/**
 * A chat switch renders the new chat after the click's own task, not in it.
 *
 * activeChatId is store state, and a store change renders synchronously, in a
 * microtask at the end of the click. The chat stream's fetch is a few promise
 * hops past selectChat (the interceptor chain, the token), so when the new
 * chat's ChatContainer mounted in that microtask (75-250ms for a cached
 * transcript), the stream request left the browser only after it. ChatInterface
 * defers the mount, so every request the click started goes out first.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StoreApi } from "zustand";

const h = vi.hoisted(() => ({
  log: [] as string[],
  /** Resolvers waiting for a view to commit, by its tabId (or "new-chat"). */
  waiters: new Map<string, () => void>(),
}));

const store = vi.hoisted(() => ({ current: null as unknown }));

vi.mock("../../../store/chatStoreHooks", async () => {
  const { create } = await import("zustand");
  const s = create<{ activeChatId: string | null }>(() => ({ activeChatId: "chat-a" }));
  store.current = s;
  return {
    useActiveChatId: () => s((state) => state.activeChatId),
    useActiveChat: () => {
      const id = s((state) => state.activeChatId);
      return id ? { id } : null;
    },
    useChat: (id: string | undefined) => (id ? { id } : undefined),
  };
});

vi.mock("../ChatContainer", async () => {
  const { useEffect } = await import("react");
  return {
    ChatContainer: ({ tabId }: { tabId: string }) => {
      h.log.push(`render ${tabId}`);
      useEffect(() => h.waiters.get(tabId)?.(), [tabId]);
      return <div data-chat={tabId} />;
    },
  };
});

vi.mock("../NewChatView", async () => {
  const { useEffect } = await import("react");
  return {
    NewChatView: () => {
      useEffect(() => h.waiters.get("new-chat")?.(), []);
      return <div data-new-chat />;
    },
  };
});

import { ChatInterface } from "../ChatInterface";

type ActiveStore = StoreApi<{ activeChatId: string | null }>;
const activeStore = () => store.current as ActiveStore;

/**
 * Resolves once the view for `key` has committed. Call it before the switch.
 *
 * The deferred chat renders in a transition, on a scheduler macrotask, and a
 * fixed sleep raced it: under load a stalled event loop ran a 20ms timer
 * before the scheduler's task, and the test asserted before chat-b rendered.
 * Awaiting the commit itself has no such window.
 */
const committed = (key: string) =>
  new Promise<void>((resolve) =>
    h.waiters.set(key, () => {
      h.waiters.delete(key);
      resolve();
    }),
  );

describe("ChatInterface on a chat switch", () => {
  let root: Root;
  let container: HTMLDivElement;
  const actEnv = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

  beforeEach(async () => {
    h.log.length = 0;
    h.waiters.clear();
    activeStore().setState({ activeChatId: "chat-a" });
    container = document.createElement("div");
    document.body.appendChild(container);
    actEnv.IS_REACT_ACT_ENVIRONMENT = true;
    root = createRoot(container);
    await act(async () => root.render(<ChatInterface />));
    // The switch itself runs as the browser runs it: no act() batching.
    actEnv.IS_REACT_ACT_ENVIRONMENT = false;
  });

  afterEach(async () => {
    actEnv.IS_REACT_ACT_ENVIRONMENT = true;
    await act(async () => root.unmount());
    container.remove();
  });

  it("lets a request the click started a few promise hops deep leave before the new chat renders", async () => {
    h.log.length = 0;
    const shown = committed("chat-b");

    // The click: selectChat sets the active chat, then starts the stream,
    // whose fetch is reached only after several awaits.
    activeStore().setState({ activeChatId: "chat-b" });
    void (async () => {
      for (let hop = 0; hop < 8; hop++) await null;
      h.log.push("fetch");
    })();

    await shown;

    // Exact order, asserted at the commit: a fetch that has not left by the
    // time chat-b renders is missing here, not merely late.
    expect(h.log).toEqual(["fetch", "render chat-b"]);
    expect(container.querySelector("[data-chat]")?.getAttribute("data-chat")).toBe("chat-b");
  });

  it("does not re-render the chat being left while the new one is pending", async () => {
    h.log.length = 0;
    const shown = committed("chat-b");
    activeStore().setState({ activeChatId: "chat-b" });
    await shown;
    expect(h.log).toEqual(["render chat-b"]);
  });

  it("shows the active chat at once when there was no chat to keep on screen", async () => {
    const newChat = committed("new-chat");
    activeStore().setState({ activeChatId: null });
    await newChat;
    expect(container.querySelector("[data-new-chat]")).not.toBeNull();
    h.log.length = 0;

    const shown = committed("chat-c");
    activeStore().setState({ activeChatId: "chat-c" });
    await shown;
    expect(container.querySelector("[data-chat]")?.getAttribute("data-chat")).toBe("chat-c");
  });
});
