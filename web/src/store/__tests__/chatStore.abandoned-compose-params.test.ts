import { describe, expect, it, beforeEach } from "vitest";
import { useChatStore } from "../chatStore";
import { useChatParamsStore } from "../chatParamsStore";
import { clearAllMessagesCache } from "../../hooks/message-queries";

// tempNewChat* is the scratch space for a chat that has not been created yet.
// Its only exit used to be transferTempToChat() on SEND, so a compose the user
// walked away from stayed in the store and ChatInput restored it for the NEXT
// new chat — a `mode` chosen once silently became the mode of every new chat for
// the rest of the session, across projects. Only a reload cleared it, which is
// why it presented as "sometimes new chats open in plan mode".
//
// selectChat() is where that abandonment is unambiguous. These tests pin BOTH
// directions, because the fix is only correct if it leaves the send path alone:
// clearing too eagerly would throw away params the user is still editing (the
// standing NOTE in NewChatView documents that failure mode — the view remounts
// between chat-creation attempts, so its lifecycle cannot express "abandoned").

const EXISTING_CHAT = { id: "c-existing" } as never;
const CREATED_CHAT = { id: "c-created" } as never;

function reset() {
  clearAllMessagesCache();
  useChatStore.setState({ activeChatId: null } as never);
  useChatParamsStore.setState({
    chatParams: {},
    chatPresets: {},
    tempNewChatParams: {},
    tempNewChatWorkflow: null,
    tempNewChatPresets: {},
  } as never);
}

describe("abandoned new-chat compose does not configure the next one", () => {
  beforeEach(reset);

  it("drops temp params when the user opens a different chat instead of sending", () => {
    // Compose a new chat, pick plan mode, then walk away without sending.
    useChatParamsStore.getState().setTempNewChatParams({ mode: "plan" });
    useChatParamsStore.getState().setTempNewChatWorkflow("builtin://agent");
    useChatParamsStore.getState().setTempNewChatPresets({ default: "researcher" });

    useChatStore.getState().selectChat(EXISTING_CHAT);

    const store = useChatParamsStore.getState();
    expect(store.tempNewChatParams).toEqual({});
    expect(store.tempNewChatWorkflow).toBeNull();
    expect(store.tempNewChatPresets).toEqual({});
  });

  it("leaves a sent chat's params intact — the send path transfers before selecting", () => {
    useChatParamsStore.getState().setTempNewChatParams({ mode: "plan" });
    useChatParamsStore.getState().setTempNewChatPresets({ default: "researcher" });

    // The order NewChatView.handleSend uses: transfer, THEN select. The clear in
    // selectChat must therefore be a no-op here rather than eating the params
    // that were just handed to the real chat.
    useChatParamsStore.getState().transferTempToChat(CREATED_CHAT.id);
    useChatStore.getState().selectChat(CREATED_CHAT);

    const store = useChatParamsStore.getState();
    expect(store.getChatParams(CREATED_CHAT.id)).toEqual({ mode: "plan" });
    expect(store.getChatPresets(CREATED_CHAT.id)).toEqual({ default: "researcher" });
    expect(store.tempNewChatParams).toEqual({});
  });

  it("does not disturb params already saved against other chats", () => {
    useChatParamsStore.getState().setChatParams("c-other", { mode: "auto" });
    useChatParamsStore.getState().setTempNewChatParams({ mode: "plan" });

    useChatStore.getState().selectChat(EXISTING_CHAT);

    const store = useChatParamsStore.getState();
    expect(store.getChatParams("c-other")).toEqual({ mode: "auto" });
    expect(store.tempNewChatParams).toEqual({});
  });
});
