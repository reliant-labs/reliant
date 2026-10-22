import { describe, it, expect } from "vitest";
import { fromJsonString } from "@bufbuild/protobuf";
import { MessageSchema, DisplayStyle } from "../../gen/reliant/v1/chat_pb";

/**
 * A live message update is a raw Go `encoding/json` payload parsed straight
 * into the proto Message (convertChatUpdateData in streaming-grpc.ts). The
 * transcript hides LLM-context-only messages by checking
 * `displayStyle === DisplayStyle.HIDDEN`, so that check only works if the
 * snake_case integer the backend emits survives this parse.
 */
describe("live message update display_style", () => {
  it("parses the backend's snake_case integer into DisplayStyle.HIDDEN", () => {
    const payload = JSON.stringify({
      id: "msg-1",
      role: 4,
      thread: "chat-1",
      display_style: DisplayStyle.HIDDEN,
      content_blocks: [],
    });

    const parsed = fromJsonString(MessageSchema, payload, {
      ignoreUnknownFields: true,
    });

    expect(parsed.displayStyle).toBe(DisplayStyle.HIDDEN);
  });

  it("leaves displayStyle unset when the backend omits it", () => {
    const parsed = fromJsonString(
      MessageSchema,
      JSON.stringify({ id: "msg-2", role: 1, thread: "chat-1" }),
      { ignoreUnknownFields: true },
    );

    expect(parsed.displayStyle).toBeUndefined();
  });
});
