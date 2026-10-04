// Copyright (c) 2025 Reliant Labs

/**
 * The launch event's free-form payload, read into the fields run detail shows,
 * and the prompt a run started with, read back from its own first messages.
 * The payload is recorded verbatim at launch, so every field is optional and
 * a missing one must read as absent, never as a wrong value.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { TriggerEventKind, TriggerEventSchema } from "@/gen/reliant/v1/trigger_pb";
import { ContentBlockType, MessageRole } from "@/gen/reliant/v1/chat_pb";
import type { Message } from "@/types/chat";
import { buildListRunsRequest, firstPromptOf, launchEventFromProto } from "../run-grpc";

describe("launchEventFromProto", () => {
  it("reads a schedule launch: slot, automation name, manual flag and the start", () => {
    const event = launchEventFromProto(
      create(TriggerEventSchema, {
        kind: TriggerEventKind.SCHEDULE,
        triggerId: "trig-1",
        occurredAt: "2026-10-04T09:00:00Z",
        payload: {
          scheduled_for: "2026-10-04T09:00:00Z",
          trigger_name: "Nightly triage",
          manual: true,
          seed_fingerprint: "abc",
          start: {
            workflow: "triage",
            presets: { "": "careful", review: "strict" },
            params: { depth: 3, review: { strictness: "high" } },
          },
        },
      }),
    );
    expect(event).toEqual({
      kind: "schedule",
      triggerId: "trig-1",
      occurredAt: "2026-10-04T09:00:00Z",
      scheduledFor: "2026-10-04T09:00:00Z",
      triggerName: "Nightly triage",
      manual: true,
      parentChatId: undefined,
      start: {
        workflow: "triage",
        presets: { "": "careful", review: "strict" },
        params: { depth: 3, review: { strictness: "high" } },
      },
    });
  });

  it("reads the recorded prompt and the time the fire actually ran", () => {
    const event = launchEventFromProto(
      create(TriggerEventSchema, {
        kind: TriggerEventKind.SCHEDULE,
        payload: {
          scheduled_for: "2026-10-04T09:00:00Z",
          fired_at: "2026-10-04T09:04:00Z",
          start: { workflow: "triage", presets: {}, params: {}, prompt: "Triage the new issues" },
        },
      }),
    );
    expect(event.firedAt).toBe("2026-10-04T09:04:00Z");
    expect(event.start?.prompt).toBe("Triage the new issues");
  });

  it("reads absent prompt and fired_at as absent", () => {
    const event = launchEventFromProto(
      create(TriggerEventSchema, {
        kind: TriggerEventKind.SCHEDULE,
        payload: { start: { workflow: "triage", presets: {}, params: {} } },
      }),
    );
    expect(event.firedAt).toBeUndefined();
    expect(event.start?.prompt).toBeUndefined();
  });

  it("reads an agent launch's parent chat", () => {
    const event = launchEventFromProto(
      create(TriggerEventSchema, {
        kind: TriggerEventKind.AGENT_START_RUN,
        payload: { parent_chat_id: "parent-1", start: { workflow: "builtin://agent", presets: {}, params: {} } },
      }),
    );
    expect(event.kind).toBe("agent.start_run");
    expect(event.parentChatId).toBe("parent-1");
    expect(event.manual).toBe(false);
  });

  it("an older payload with no start reads as no start, not an empty one", () => {
    const event = launchEventFromProto(
      create(TriggerEventSchema, { kind: TriggerEventKind.CHAT_START, payload: { message_count: 1 } }),
    );
    expect(event.kind).toBe("chat.start");
    expect(event.start).toBeUndefined();
    expect(event.scheduledFor).toBeUndefined();
  });
});

function message(overrides: Partial<Message>): Message {
  return {
    id: "m",
    chatId: "chat-1",
    seq: 0n,
    thread: "",
    role: MessageRole.USER,
    contentBlocks: [],
    attachments: [],
    createdAt: "",
    updatedAt: "",
    streamingState: 0,
    sequenceNumber: 0n,
    ...overrides,
  } as Message;
}

function text(content: string) {
  return [{ id: "", index: 0, type: ContentBlockType.TEXT, content }] as Message["contentBlocks"];
}

describe("firstPromptOf", () => {
  it("is the earliest user message's text, skipping the hidden system seed", () => {
    const prompt = firstPromptOf(
      [
        message({ seq: 2n, role: MessageRole.ASSISTANT, contentBlocks: text("On it.") }),
        message({ seq: 1n, contentBlocks: text("Triage the new issues") }),
        message({ seq: 0n, role: MessageRole.SYSTEM, contentBlocks: text("No human is watching") }),
        message({ seq: 3n, contentBlocks: text("And the old ones") }),
      ],
      "chat-1",
    );
    expect(prompt).toBe("Triage the new issues");
  });

  it("ignores rows a branch inherited from its parent chat", () => {
    const prompt = firstPromptOf(
      [
        message({ seq: 0n, chatId: "parent", contentBlocks: text("The parent's prompt") }),
        message({ seq: 9n, chatId: "chat-1", contentBlocks: text("The branch's prompt") }),
      ],
      "chat-1",
    );
    expect(prompt).toBe("The branch's prompt");
  });

  it("is undefined when the window holds no user text", () => {
    expect(firstPromptOf([], "chat-1")).toBeUndefined();
    expect(firstPromptOf([message({ contentBlocks: [] })], "chat-1")).toBeUndefined();
  });
});

describe("buildListRunsRequest: parent", () => {
  it("narrows to one chat's children", () => {
    const request = buildListRunsRequest({ parentChatId: "parent-1" }, { now: Date.now() });
    expect(request.parentChatId).toBe("parent-1");
  });
});
