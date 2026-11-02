import { describe, expect, it } from "vitest";
import { ToolCallStatus } from '../../../gen/reliant/v1/chat_pb';
import { isQuietToolRun, transcriptDateLabels } from "./transcriptPresentation";

it('labels local date transitions by original message id across prepend', () => {
  const first = { id: 'first', createdAt: '2024-01-02T12:00:00' };
  const second = { id: 'second', createdAt: '2024-01-02T13:00:00' };
  const next = { id: 'next', createdAt: '2024-01-03T12:00:00' };
  expect([...transcriptDateLabels([first, second, next]).keys()]).toEqual(['first', 'next']);
  const older = { id: 'older', createdAt: '2024-01-02T10:00:00' };
  expect([...transcriptDateLabels([older, first, second, next]).keys()]).toEqual(['older', 'next']);
  expect(transcriptDateLabels([{ id: 'bad', createdAt: 'invalid' }]).size).toBe(0);
});

describe("quiet transcript activity", () => {
  const completed = { call: {}, result: { is_error: false }, status: "completed" };
  it("only collapses multiple settled successful calls", () => {
    expect(isQuietToolRun([completed, completed])).toBe(true);
    expect(isQuietToolRun([completed])).toBe(false);
    expect(isQuietToolRun([])).toBe(false);
  });
  it.each([
    { ...completed, result: undefined },
    { ...completed, result: { is_error: true } },
    { ...completed, approval: { status: "pending" } },
    { ...completed, status: "executing" },
    { ...completed, status: "failed" },
    { ...completed, status: undefined, call: { durableStatus: ToolCallStatus.FAILED } },
    { ...completed, status: "backgrounded" },
    { ...completed, call: { childWorkflowId: "child" } },
  ])("keeps actionable, failed and live activity visible: %j", (execution) => {
    expect(isQuietToolRun([completed, execution])).toBe(false);
  });
  it("does not mutate original ownership or block indexes", () => {
    const call = Object.freeze({ id: "tool", content_block_id: "block", index: 9 });
    const execution = Object.freeze({ call, result: Object.freeze({ is_error: false }) });
    const run = Object.freeze([execution, execution]);
    expect(isQuietToolRun(run)).toBe(true);
    expect(run[0]).toBe(execution);
    expect(run[0].call).toBe(call);
    expect(call.index).toBe(9);
  });
});
