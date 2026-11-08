import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ApprovalSchema, ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import type { ToolExecution } from "../../../lib/messageProcessor";
import { createSurfacedPredicate, isSurfacedExecution } from "./surfacedExecutions";

const call = (name: string, input: Record<string, unknown> = {}) => ({ name, input });
const ok = (content = "done") => ({ content, is_error: false });

describe("isSurfacedExecution", () => {
  it.each([
    ["ask_user, unanswered", { call: call("ask_user") }],
    ["ask_user, answered", { call: call("ask_user"), result: ok() }],
    ["request_machine", { call: call("request_machine") }],
    ["generate_image, still running", { call: call("generate_image") }],
    ["generate_video", { call: call("mcp__reliant__generate_video"), result: ok() }],
    ["a call awaiting approval", { call: call("bash"), approval: { status: ApprovalStatus.PENDING } }],
    ["a drafted automation", { call: call("draft_automation"), result: ok() }],
    [
      "a deploy that put an app live",
      {
        call: call("shell", { command: "reliant forge env deploy prod" }),
        result: ok(JSON.stringify({ exit_code: 0, output: "released" })),
      },
    ],
  ])("keeps %s on screen", (_name, execution) => {
    expect(isSurfacedExecution(execution)).toBe(true);
  });

  it.each([
    ["a shell command", { call: call("bash"), result: ok() }],
    ["a screenshot", { call: call("mcp__chrome-devtools__take_screenshot"), result: ok() }],
    ["a settled approval", { call: call("bash"), approval: { status: ApprovalStatus.APPROVED } }],
    ["a draft still being written", { call: call("draft_automation") }],
    ["a failed draft", { call: call("draft_automation"), result: { content: "bad", is_error: true } }],
    ["a deploy still running", { call: call("shell", { command: "reliant forge env deploy prod" }) }],
    ["a spawn", { call: call("spawn"), result: ok() }],
  ])("folds %s", (_name, execution) => {
    expect(isSurfacedExecution(execution)).toBe(false);
  });
});

describe("createSurfacedPredicate", () => {
  const execution = (name: string, input: Record<string, unknown> = {}): ToolExecution =>
    ({ call: { id: `call-${name}`, name, input, content_block_id: `block-${name}` } }) as unknown as ToolExecution;

  it("joins results from the chat's index", () => {
    const deploy = execution("shell", { command: "reliant forge env deploy prod" });
    expect(createSurfacedPredicate({}, [])(deploy)).toBe(false);
    const results = { "call-shell": ok(JSON.stringify({ exit_code: 0, output: "released" })) };
    expect(createSurfacedPredicate(results as never, [])(deploy)).toBe(true);
  });

  it("joins approvals by content block id", () => {
    const pending = create(ApprovalSchema, { id: "a1", entityId: "block-bash", status: ApprovalStatus.PENDING });
    expect(createSurfacedPredicate({}, [pending])(execution("bash"))).toBe(true);
    expect(createSurfacedPredicate({}, [])(execution("bash"))).toBe(false);
  });
});
