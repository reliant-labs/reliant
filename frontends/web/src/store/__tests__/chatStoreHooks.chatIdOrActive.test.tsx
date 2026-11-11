import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useChatStore } from "../chatStore";
import { useChatIdOrActive } from "../chatStoreHooks";

// A chat switch must not re-render the rows of the chat being left: a message
// bound to its chat ignores the active chat.

afterEach(() => {
  act(() => useChatStore.setState({ activeChatId: null }));
});

describe("useChatIdOrActive", () => {
  it("is the given chat, and does not re-render when the active chat changes", () => {
    act(() => useChatStore.setState({ activeChatId: "chat-a" }));
    let renders = 0;
    const { result } = renderHook(() => {
      renders++;
      return useChatIdOrActive("chat-x");
    });
    expect(result.current).toBe("chat-x");
    const before = renders;

    act(() => useChatStore.setState({ activeChatId: "chat-b" }));

    expect(result.current).toBe("chat-x");
    expect(renders).toBe(before);
  });

  it("follows the active chat when given none", () => {
    act(() => useChatStore.setState({ activeChatId: "chat-a" }));
    const { result } = renderHook(() => useChatIdOrActive(undefined));
    expect(result.current).toBe("chat-a");

    act(() => useChatStore.setState({ activeChatId: "chat-b" }));
    expect(result.current).toBe("chat-b");
  });
});
