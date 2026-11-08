// @vitest-environment node
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  AgentMessageKind,
  MessageSenderSchema,
  ContentBlockType,
  DisplayStyle,
  MessageRole,
  MessageSchema,
  type Message,
} from "../../../gen/reliant/v1/chat_pb";

/** Message fields a fixture may override (a protobuf init, not a full message). */
type MessageExtra = Omit<Partial<Message>, "$typeName" | "$unknown">;
import { buildSimplifiedTimeline, createSegmentReader, isSurfacedMediaTool } from "./simplifiedTurns";

type Block = { t: string } | { call: string };
type Item =
  | { type: "message"; message: Message; workflow: { thread: string } }
  | { type: "thread-start" }
  | { type: "handoff" }
  | { type: "error" }
  | { type: "info" };

function msg(
  id: string,
  role: MessageRole,
  blocks: Block[],
  extra: MessageExtra = {},
): Message {
  return create(MessageSchema, {
    id,
    role,
    contentBlocks: blocks.map((b, index) =>
      "t" in b
        ? { id: `${id}-b${index}`, index, type: ContentBlockType.TEXT, content: b.t }
        : {
            id: `${id}-b${index}`,
            index,
            type: ContentBlockType.TOOL_CALL,
            toolName: "bash",
            toolCallId: b.call,
            input: "{}",
          },
    ),
    ...extra,
  });
}

const asst = (id: string, blocks: Block[], extra: MessageExtra = {}) =>
  msg(id, MessageRole.ASSISTANT, blocks, extra);

function row(message: Message, thread = "t1"): Item {
  return { type: "message", message, workflow: { thread } };
}

function build(items: Item[]) {
  return buildSimplifiedTimeline(items, {
    segmentsOf: createSegmentReader(),
    workflowOf: (item) => item.workflow,
  });
}

type Sliced = Extract<Item, { type: "message" }> & {
  slice?: { segmentIndexes: readonly number[]; role: string; chrome?: boolean };
  key?: string;
};

/**
 * One line per row: `work:<key>[<message>:<segments> ...]+<events>`,
 * `prose:<message>:<segments>` (`*` = carries the chrome, `@key` = its own
 * row key), or `msg:<message>` for a whole message.
 */
function summarize(out: ReturnType<typeof build>) {
  return out.map((o) => {
    if (o.type === "work") {
      const events = o.work.events?.length ? `+${o.work.events.map((e) => `${e.message.id}@${e.afterPart}`).join(",")}` : "";
      return `work:${o.work.key}[${o.work.parts
        .map((p) => `${p.message.id}:${p.segmentIndexes.join(",")}`)
        .join(" ")}]${events}`;
    }
    if (o.type === "message") {
      const m = o as Sliced;
      if (!m.slice) return `msg:${m.message.id}`;
      return `${m.slice.role}:${m.message.id}:${m.slice.segmentIndexes.join(",")}${m.slice.chrome ? "*" : ""}${m.key ? `@${m.key}` : ""}`;
    }
    return o.type;
  });
}

const sender = (kind: AgentMessageKind) => create(MessageSenderSchema, { threadId: "child", kind });
const mail = (id: string, extra: MessageExtra = {}) =>
  row(msg(id, MessageRole.USER, [{ t: `report ${id}` }], { sender: sender(AgentMessageKind.MESSAGE), ...extra }));
const notice = (id = "notice", text = 'spawn "research" completed', extra: MessageExtra = {}) =>
  row(msg(id, MessageRole.SYSTEM, [{ t: text }], { displayStyle: DisplayStyle.INFO, ...extra }));

describe("buildSimplifiedTimeline", () => {
  it("never folds prose: a turn reads as its narration with the tool run folded between", () => {
    const user = row(msg("u1", MessageRole.USER, [{ t: "go" }]));
    const a1 = asst("a1", [{ t: "Looking" }, { call: "c1" }]);
    const a2 = asst("a2", [{ call: "c2" }]);
    const a3 = asst("a3", [{ t: "Done." }]);
    const items = [user, row(a1), row(a2), row(a3)];
    const out = build(items);
    expect(summarize(out)).toEqual(["msg:u1", "prose:a1:0*", "work:work-a1-1[a1:1 a2:0]", "msg:a3"]);
    // The text-only answer is the detailed row itself.
    expect(out[3]).toBe(items[3]);
    const work = out[2] as { workflow: { thread: string }; work: { thread: string } };
    expect(work.workflow).toEqual({ thread: "t1" });
    expect(work.work.thread).toBe("t1");
  });

  it("cuts one message into prose / work / prose, with the chrome on its last prose slice only", () => {
    const a1 = asst("a1", [{ call: "c1" }, { t: "intermediate" }, { call: "c2" }, { t: "final" }]);
    expect(summarize(build([row(a1)]))).toEqual([
      "work:work-a1-0[a1:0]",
      "prose:a1:1",
      "work:work-a1-2[a1:2]",
      "prose:a1:3*@a1:3",
    ]);
  });

  it("gives every row a distinct key", () => {
    const a1 = asst("a1", [{ t: "one" }, { call: "c1" }, { t: "two" }, { call: "c2" }, { t: "three" }]);
    const keys = build([row(a1)]).map((o) => (o.type === "work" ? o.work.key : ((o as Sliced).key ?? (o as Sliced).message.id)));
    expect(keys).toEqual(["a1", "work-a1-1", "a1:2", "work-a1-3", "a1:4"]);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it("ends on the open run while a turn is still working", () => {
    const a1 = asst("a1", [{ t: "Checking" }, { call: "c1" }]);
    expect(summarize(build([row(a1)]))).toEqual(["prose:a1:0*", "work:work-a1-1[a1:1]"]);
  });

  it("emits text-only messages unchanged, by identity", () => {
    const items = [row(asst("a1", [{ t: "hi" }])), row(asst("a2", [{ t: "there" }]))];
    const out = build(items);
    expect(out).toHaveLength(2);
    expect(out[0]).toBe(items[0]);
    expect(out[1]).toBe(items[1]);
  });

  it("passes non-message items and user rows through by identity", () => {
    const items: Item[] = [
      { type: "thread-start" },
      row(msg("u1", MessageRole.USER, [{ t: "go" }])),
      { type: "info" },
    ];
    const out = build(items);
    expect(out.length).toBe(3);
    out.forEach((o, i) => expect(o).toBe(items[i]));
  });

  it("keeps the work key while the run grows at its end", () => {
    const a1 = asst("a1", [{ t: "Starting" }, { call: "c1" }]);
    const before = build([row(a1)]);
    const after = build([row(a1), row(asst("a2", [{ call: "c2" }])), row(asst("a3", [{ call: "c3" }]))]);
    expect(summarize(after)).toEqual(["prose:a1:0*", "work:work-a1-1[a1:1 a2:0 a3:0]"]);
    expect((before[1] as { work: { key: string } }).work.key).toBe((after[1] as { work: { key: string } }).work.key);
  });

  it("keeps a message's first prose row on the message's own key when it starts calling tools", () => {
    const streaming = row(asst("a1", [{ t: "Let me check" }]));
    expect(build([streaming])[0]).toBe(streaming);
    const out = build([row(asst("a1", [{ t: "Let me check" }, { call: "c1" }]))]);
    expect((out[0] as Sliced).key).toBeUndefined();
  });

  it("folds a blank assistant message into the open run, so the live fold stays last", () => {
    const out = build([row(asst("a1", [{ call: "c1" }])), row(asst("a2", [{ t: "  " }]))]);
    expect(summarize(out)).toEqual(["work:work-a1-0[a1:0 a2:0]"]);
    // With no run open it is left alone.
    const lone = row(asst("a3", [{ t: "  " }]));
    expect(build([lone])[0]).toBe(lone);
  });

  it("is stable across rebuilds", () => {
    const items = [row(asst("a1", [{ call: "c1" }])), row(asst("a2", [{ t: "ok" }]))];
    expect(summarize(build(items))).toEqual(summarize(build(items)));
    expect((build(items)[0] as { work: { key: string } }).work.key).toBe("work-a1-0");
  });

  describe("boundaries", () => {
    const work = (id: string) => row(asst(id, [{ call: `${id}-c` }, { t: `${id} text` }]));

    it.each([
      ["user message", row(msg("u", MessageRole.USER, [{ t: "x" }]))],
      ["thread-start", { type: "thread-start" } as Item],
      ["handoff", { type: "handoff" } as Item],
      ["error", { type: "error" } as Item],
      [
        "displayStyle message",
        row(asst("sys", [{ t: "compacted" }], { displayStyle: DisplayStyle.INFO })),
      ],
    ])("a %s ends the run", (_name, boundary) => {
      const tools = (id: string) => row(asst(id, [{ call: `${id}-c` }]));
      const out = build([tools("a1"), boundary, tools("a2")]);
      expect(summarize(out).filter((x) => x.startsWith("work:"))).toEqual([
        "work:work-a1-0[a1:0]",
        "work:work-a2-0[a2:0]",
      ]);
      expect(out[1]).toBe(boundary);
    });

    it("prose ends the run", () => {
      expect(summarize(build([work("a1"), work("a2")]))).toEqual([
        "work:work-a1-0[a1:0]",
        "prose:a1:1*",
        "work:work-a2-0[a2:0]",
        "prose:a2:1*",
      ]);
    });

    it("a thread switch ends the run", () => {
      const s = summarize(build([row(asst("a1", [{ call: "c" }])), row(asst("b1", [{ call: "c" }]), "t2")]));
      expect(s).toEqual(["work:work-a1-0[a1:0]", "work:work-b1-0[b1:0]"]);
    });
  });

  it("keeps two interleaved threads as separate runs", () => {
    const out = build([
      row(asst("a1", [{ call: "c1" }]), "t1"),
      row(asst("b1", [{ call: "c2" }]), "t2"),
      row(asst("a2", [{ t: "A done" }]), "t1"),
      row(asst("b2", [{ t: "B done" }]), "t2"),
    ]);
    expect(summarize(out)).toEqual([
      "work:work-a1-0[a1:0]",
      "work:work-b1-0[b1:0]",
      "msg:a2",
      "msg:b2",
    ]);
  });

  describe("agent mail and notices", () => {
    it("folds an orchestrator's alternating tool runs and sub-agent mail into one row", () => {
      const out = build([
        row(asst("a1", [{ call: "spawn-1" }])),
        mail("m1"),
        row(asst("a2", [{ call: "c2" }])),
        mail("m2"),
        mail("m3"),
        row(asst("a3", [{ call: "c3" }])),
        row(asst("a4", [{ t: "All three agents reported back." }])),
      ]);
      expect(summarize(out)).toEqual([
        "work:work-a1-0[a1:0 a2:0 a3:0]+m1@1,m2@2,m3@2",
        "msg:a4",
      ]);
    });

    it("folds two or more mail messages with no tool work between", () => {
      expect(summarize(build([mail("m1"), mail("m2"), notice("n1")]))).toEqual([
        "work:work-m1[]+m1@0,m2@0,n1@0",
      ]);
    });

    it("leaves a lone message with no tool work as its own row", () => {
      for (const lone of [mail("m1"), notice()]) {
        expect(build([lone])[0]).toBe(lone);
        const text = row(asst("text", [{ t: "A conversational answer" }]));
        const out = build([text, lone, text]);
        expect(out[1]).toBe(lone);
      }
    });

    it.each(['spawn "research" completed', 'spawn "say \\"hello\\"" completed'])(
      "retains exact legacy completion %s inside the run without fragmenting it", (text) => {
        const event = notice("notice", text);
        const out = build([row(asst("a1", [{ call: "c1" }])), event, row(asst("a2", [{ call: "c2" }])), row(asst("answer", [{ t: "Done" }]))]);
        expect(summarize(out)).toEqual(["work:work-a1-0[a1:0 a2:0]+notice@1", "msg:answer"]);
        expect(out[0].type === "work" && out[0].work.events).toEqual([
          { type: "message", message: (event as Extract<Item, { type: "message" }>).message, afterPart: 1 },
        ]);
      },
    );

    it("folds typed completions", () => {
      const event = notice("n", "Result ready", { sender: sender(AgentMessageKind.COMPLETION) });
      expect(summarize(build([row(asst("a1", [{ t: "Investigating" }, { call: "c1" }])), event]))).toEqual([
        "prose:a1:0*",
        "work:work-a1-1[a1:1]+n@1",
      ]);
    });

    it.each([
      ["a legacy failure", notice("e", 'spawn "research" failed')],
      ["a legacy cancellation", notice("e", 'spawn "research" cancelled')],
      ["an unclassified notice", notice("e", "Other operation completed")],
      ["a warning", notice("e", 'spawn "research" completed', { displayStyle: DisplayStyle.WARNING })],
      ["a typed failure", notice("e", 'spawn "research" failed', { sender: sender(AgentMessageKind.FAILED) })],
      ["a typed cancellation", mail("e", { sender: sender(AgentMessageKind.CANCELLED) })],
      ["warning mail", mail("e", { displayStyle: DisplayStyle.WARNING })],
      ["mail with attachments", mail("e", { attachments: [{ id: "att" } as never] })],
      ["the human's own message", row(msg("e", MessageRole.USER, [{ t: "stop" }]))],
    ])("keeps %s as a boundary", (_name, event) => {
      const out = build([row(asst("a1", [{ call: "c1" }])), event, row(asst("a2", [{ call: "c2" }]))]);
      expect(out[1]).toBe(event);
      expect(out.filter((item) => item.type === "work")).toHaveLength(2);
    });

    it.each([false, true])("keeps cross-thread completion metadata outside both adjacent folds (typed=%s)", (typed) => {
      const event = notice("notice", 'spawn "research" completed', {
        thread: "other-thread",
        ...(typed ? { sender: sender(AgentMessageKind.COMPLETION) } : {}),
      });
      // Its timeline workflow is the parent, but the durable message belongs
      // elsewhere. It must remain a boundary rather than join either fold.
      const out = build([
        row(asst("before", [{ call: "c1" }], { thread: "t1" })),
        event,
        row(asst("after", [{ call: "c2" }], { thread: "t1" })),
      ]);
      expect(out[1]).toBe(event);
      const folds = out.filter((item) => item.type === "work");
      expect(folds).toHaveLength(2);
      for (const fold of folds) {
        if (fold.type === "work") expect(fold.work.events).toBeUndefined();
      }
    });

    it("does not fold mail into another thread's run", () => {
      const out = build([row(asst("a1", [{ call: "c1" }]), "t2"), mail("m1")]);
      expect(summarize(out)).toEqual(["work:work-a1-0[a1:0]", "msg:m1"]);
    });
  });

  it("keeps attachments visible on a message that only contributes work", () => {
    const a1 = asst("a1", [{ call: "c1" }], {
      attachments: [{ id: "att" } as never],
    });
    expect(summarize(build([row(a1)]))).toEqual(["work:work-a1-0[a1:0]", "prose:a1:*"]);
  });
});

describe("isSurfacedMediaTool", () => {
  it("surfaces only the generation tools' media", () => {
    expect(isSurfacedMediaTool("generate_image")).toBe(true);
    expect(isSurfacedMediaTool("generate_video")).toBe(true);
    expect(isSurfacedMediaTool("mcp__reliant__generate_image")).toBe(true);
    expect(isSurfacedMediaTool("mcp__chrome-devtools__take_screenshot")).toBe(false);
    expect(isSurfacedMediaTool("view")).toBe(false);
  });
});
