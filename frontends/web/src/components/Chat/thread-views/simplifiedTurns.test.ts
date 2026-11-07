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
import { buildSimplifiedTimeline, createSegmentReader } from "./simplifiedTurns";

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

function build(items: Item[], live = false) {
  return buildSimplifiedTimeline(items, {
    segmentsOf: createSegmentReader(),
    isLiveTurn: () => live,
    workflowOf: (item) => item.workflow,
  });
}

function summarize(out: ReturnType<typeof build>) {
  return out.map((o) => {
    if (o.type === "work") {
      return `work:${o.work.key}[${o.work.parts
        .map((p) => `${p.message.id}:${p.segmentIndexes.join(",")}`)
        .join(" ")}]`;
    }
    if (o.type === "message") {
      const m = (o as Extract<Item, { type: "message" }> & { slice?: { segmentIndexes: readonly number[] } });
      return `msg:${m.message.id}${m.slice ? `:${m.slice.segmentIndexes.join(",")}` : ""}`;
    }
    return o.type;
  });
}

describe("buildSimplifiedTimeline", () => {
  it("folds a multi-message turn into one work item plus the answer", () => {
    const a1 = asst("a1", [{ t: "Looking" }, { call: "c1" }]);
    const a2 = asst("a2", [{ call: "c2" }]);
    const a3 = asst("a3", [{ t: "Done." }]);
    const user = row(msg("u1", MessageRole.USER, [{ t: "go" }]));
    const out = build([user, row(a1), row(a2), row(a3)]);
    expect(summarize(out)).toEqual(["msg:u1", "work:work-a1[a1:0,1 a2:0]", "msg:a3:0"]);
    const answer = out[2] as { slice: { role: string } };
    expect(answer.slice.role).toBe("answer");
    const work = out[1] as { workflow: { thread: string }; work: { thread: string } };
    expect(work.workflow).toEqual({ thread: "t1" });
    expect(work.work.thread).toBe("t1");
  });

  it("folds interim narration and keeps only text after the last tools", () => {
    const a1 = asst("a1", [{ call: "c1" }, { t: "intermediate" }, { call: "c2" }, { t: "final" }]);
    expect(summarize(build([row(a1)]))).toEqual(["work:work-a1[a1:0,1,2]", "msg:a1:3"]);
  });

  it("emits no answer while a turn ending on tools is live", () => {
    const a1 = asst("a1", [{ t: "Checking" }, { call: "c1" }]);
    expect(summarize(build([row(a1)], true))).toEqual(["work:work-a1[a1:0,1]"]);
  });

  it("promotes the last narration to the answer when a tools-ending turn is not live", () => {
    const a1 = asst("a1", [{ t: "first" }, { call: "c1" }]);
    const a2 = asst("a2", [{ t: "last words" }, { call: "c2" }]);
    expect(summarize(build([row(a1), row(a2)], false))).toEqual([
      "work:work-a1[a1:0,1 a2:1]",
      "msg:a2:0",
    ]);
  });

  it("does not invent an answer for a stopped turn with no narration", () => {
    const a1 = asst("a1", [{ call: "c1" }]);
    expect(summarize(build([row(a1)], false))).toEqual(["work:work-a1[a1:0]"]);
  });

  it("emits a text-only turn unchanged, by identity", () => {
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

  it("keeps the work key when a trailing text becomes work as the turn streams", () => {
    const a1 = asst("a1", [{ call: "c1" }, { t: "so far" }]);
    const before = build([row(a1)], true);
    expect(summarize(before)).toEqual(["work:work-a1[a1:0]", "msg:a1:1"]);

    const a2 = asst("a2", [{ call: "c2" }]);
    const after = build([row(a1), row(a2)], true);
    expect(summarize(after)).toEqual(["work:work-a1[a1:0,1 a2:0]"]);
    expect((before[0] as { work: { key: string } }).work.key).toBe(
      (after[0] as { work: { key: string } }).work.key,
    );
  });

  it("is stable across rebuilds", () => {
    const items = [row(asst("a1", [{ call: "c1" }])), row(asst("a2", [{ t: "ok" }]))];
    const first = build(items);
    const second = build(items);
    expect(summarize(second)).toEqual(summarize(first));
    expect((second[0] as { work: { key: string } }).work.key).toBe("work-a1");
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
    ])("a %s ends the turn", (_name, boundary) => {
      const out = build([work("a1"), boundary, work("a2")]);
      const s = summarize(out);
      expect(s.filter((x) => x.startsWith("work:"))).toEqual([
        "work:work-a1[a1:0]",
        "work:work-a2[a2:0]",
      ]);
      expect(out).toContain(boundary);
      expect(s.indexOf("work:work-a2[a2:0]")).toBeGreaterThan(out.indexOf(boundary));
    });

    it("a thread switch ends the turn", () => {
      const s = summarize(build([work("a1"), row(asst("b1", [{ call: "c" }]), "t2")]));
      expect(s).toEqual(["work:work-a1[a1:0]", "msg:a1:1", "work:work-b1[b1:0]"]);
    });
  });

  it("keeps two interleaved threads as separate turns", () => {
    const out = build([
      row(asst("a1", [{ call: "c1" }]), "t1"),
      row(asst("b1", [{ call: "c2" }]), "t2"),
      row(asst("a2", [{ t: "A done" }]), "t1"),
      row(asst("b2", [{ t: "B done" }]), "t2"),
    ]);
    expect(summarize(out)).toEqual([
      "work:work-a1[a1:0]",
      "work:work-b1[b1:0]",
      "msg:a2",
      "msg:b2",
    ]);
  });

  const notice = (text = 'spawn "research" completed', extra: MessageExtra = {}) =>
    row(msg("notice", MessageRole.SYSTEM, [{ t: text }], { displayStyle: DisplayStyle.INFO, ...extra }));

  it.each(['spawn "research" completed', 'spawn "say \\"hello\\"" completed'])(
    "retains exact legacy completion %s inside activity without fragmenting it", (text) => {
      const event = notice(text);
      const out = build([row(asst("a1", [{ call: "c1" }])), event, row(asst("a2", [{ call: "c2" }])), row(asst("answer", [{ t: "Done" }]))]);
      expect(summarize(out)).toEqual(["work:work-a1[a1:0 a2:0]", "msg:answer:0"]);
      expect(out[0].type === "work" && out[0].work.events).toEqual([
        { type: "message", message: (event as Extract<Item, { type: "message" }>).message, afterPart: 1 },
      ]);
    },
  );

  it("uses typed success metadata and does not promote live narration after a notice", () => {
    const event = notice("Result ready", { sender: create(MessageSenderSchema, { threadId: "child", kind: AgentMessageKind.COMPLETION }) });
    const out = build([row(asst("a1", [{ t: "Investigating" }, { call: "c1" }])), event], true);
    expect(summarize(out)).toEqual(["work:work-a1[a1:0,1]"]);
    expect(out[0].type === "work" && out[0].work.events?.length).toBe(1);
  });

  it.each([
    notice('spawn "research" failed'),
    notice('spawn "research" cancelled'),
    notice('Other operation completed'),
    notice('spawn "research" completed', { displayStyle: DisplayStyle.WARNING }),
    notice('spawn "research" completed', { sender: create(MessageSenderSchema, { threadId: "child", kind: AgentMessageKind.FAILED }) }),
    row(msg("mail", MessageRole.USER, [{ t: "Need your approval" }], { sender: create(MessageSenderSchema, { threadId: "child", kind: AgentMessageKind.MESSAGE }) })),
  ])("keeps attention and unclassified messages as boundaries %#", (event) => {
    const out = build([row(asst("a1", [{ call: "c1" }])), event, row(asst("a2", [{ call: "c2" }]))]);
    expect(out).toContain(event);
    expect(out.filter((item) => item.type === "work")).toHaveLength(2);
  });

  it("preserves orphan, text-only, and other-thread notices by identity", () => {
    const event = notice();
    expect(build([event])[0]).toBe(event);
    expect(build([row(asst("text", [{ t: "A conversational answer" }])), event])[1]).toBe(event);
    const out = build([row(asst("other", [{ call: "c1" }]), "t2"), event]);
    expect(out[1]).toBe(event);
  });

  it.each([false, true])("keeps cross-thread completion metadata outside both adjacent folds (typed=%s)", (typed) => {
    const event = notice('spawn "research" completed', {
      thread: "other-thread",
      ...(typed ? { sender: create(MessageSenderSchema, { threadId: "child", kind: AgentMessageKind.COMPLETION }) } : {}),
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

  it("keeps attachments visible on a message that only contributes work", () => {
    const a1 = asst("a1", [{ call: "c1" }], {
      attachments: [{ id: "att" } as never],
    });
    expect(summarize(build([row(a1)], true))).toEqual(["work:work-a1[a1:0]", "msg:a1:"]);
  });
});
