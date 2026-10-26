// Copyright (c) 2025 Reliant Labs

/**
 * The Runs filters, as the user sets them in the URL, become exactly the
 * ListRunsRequest fields the server filters on. A filter that maps to the
 * wrong field silently returns the wrong runs, so each one is pinned.
 */

import { describe, expect, it } from "vitest";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { buildListRunsRequest, runFromProto, RUN_STATE_FILTERS, unlaunchedEventFromProto } from "../run-queries";
import { create } from "@bufbuild/protobuf";
import { RunSchema, UnlaunchedTriggerEventOutcome, UnlaunchedTriggerEventSchema } from "@/gen/reliant/v1/run_pb";
import { ChatActivity, WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";

const NOW = Date.parse("2026-10-04T12:00:00Z");

describe("buildListRunsRequest", () => {
  it("asks for the trigger events that started no run only when no state or parent narrows the list", () => {
    expect(buildListRunsRequest({ includeUnlaunched: true }, { now: NOW }).includeUnlaunchedEvents).toBe(true);
    expect(buildListRunsRequest({}, { now: NOW }).includeUnlaunchedEvents).toBeUndefined();
    // An event has no state and no parent: the server would return none.
    expect(buildListRunsRequest({ includeUnlaunched: true, state: ["failed"] }, { now: NOW }).includeUnlaunchedEvents).toBeUndefined();
    expect(buildListRunsRequest({ includeUnlaunched: true, parentChatId: "c" }, { now: NOW }).includeUnlaunchedEvents).toBeUndefined();
  });

  it("maps an unlaunched event from the wire", () => {
    const event = unlaunchedEventFromProto(
      create(UnlaunchedTriggerEventSchema, {
        id: "e1",
        triggerId: "t1",
        triggerName: "nightly",
        workflowName: "triage",
        projectId: "p1",
        launchKind: "schedule",
        occurredAtMs: BigInt(1_000),
        outcome: UnlaunchedTriggerEventOutcome.SKIPPED,
        outcomeDetail: "previous run still going",
      }),
    );
    expect(event).toEqual({
      id: "e1",
      triggerId: "t1",
      triggerName: "nightly",
      workflowName: "triage",
      workflowTrigger: "",
      projectId: "p1",
      launchKind: "schedule",
      occurredAt: 1_000,
      outcome: "skipped",
      outcomeDetail: "previous run still going",
    });
  });

  it("defaults to the last 24 hours, every kind, archived runs included", () => {
    const request = buildListRunsRequest({}, { now: NOW });
    expect(timestampDate(request.startedAfter!).toISOString()).toBe("2026-10-03T12:00:00.000Z");
    expect(request.startedBefore).toBeUndefined();
    expect(request.launchKind).toEqual([]);
    expect(request.displayStates).toEqual([]);
    expect(request.workflow).toEqual([]);
    expect(request.projectId).toBeUndefined();
    expect(request.triggerId).toBeUndefined();
    expect(request.query).toBeUndefined();
    expect(request.pageToken).toBeUndefined();
    expect(request.includeArchived).toBe(true);
  });

  it("maps every filter onto its request field", () => {
    const request = buildListRunsRequest(
      {
        projectId: "proj-1",
        workflow: ["builtin://agent", "triage"],
        trigger: "trig-1",
        kind: ["schedule", "agent.start_run"],
        state: ["failed", "needs_you"],
        q: "  nightly ",
        range: "7d",
      },
      { now: NOW },
    );
    expect(request.projectId).toBe("proj-1");
    expect(request.workflow).toEqual(["builtin://agent", "triage"]);
    expect(request.triggerId).toBe("trig-1");
    expect(request.launchKind).toEqual(["schedule", "agent.start_run"]);
    expect(request.displayStates).toEqual([
      RunDisplayState.FAILED,
      RunDisplayState.NEEDS_INPUT,
      RunDisplayState.WAITING_FOR_MACHINE,
    ]);
    expect(request.query).toBe("nightly");
    expect(timestampDate(request.startedAfter!).toISOString()).toBe("2026-09-27T12:00:00.000Z");
  });

  it("Live covers queued, running, paused and waiting-for-machine", () => {
    const request = buildListRunsRequest({ state: ["live"] }, { now: NOW });
    expect(request.displayStates.sort()).toEqual(
      [
        RunDisplayState.QUEUED,
        RunDisplayState.RUNNING,
        RunDisplayState.PAUSED,
        RunDisplayState.WAITING_FOR_MACHINE,
      ].sort(),
    );
  });

  it("every state filter names at least one display state, and none sends UNSPECIFIED", () => {
    for (const filter of RUN_STATE_FILTERS) {
      const request = buildListRunsRequest({ state: [filter.key] }, { now: NOW });
      expect(request.displayStates.length).toBeGreaterThan(0);
      expect(request.displayStates).not.toContain(RunDisplayState.UNSPECIFIED);
    }
  });

  it("'all' time sends no lower bound", () => {
    expect(buildListRunsRequest({ range: "all" }, { now: NOW }).startedAfter).toBeUndefined();
  });

  it("an empty query is not a filter", () => {
    expect(buildListRunsRequest({ q: "   " }, { now: NOW }).query).toBeUndefined();
  });
});

describe("runFromProto", () => {
  it("keeps the run's display state and the backing chat id", () => {
    const run = runFromProto(
      create(RunSchema, {
        id: "wf-1",
        sessionId: "chat-1",
        title: "Nightly triage",
        workflowName: "builtin://agent",
        projectId: "proj-1",
        launchKind: "schedule",
        triggerId: "trig-1",
        triggerName: "Nightly",
        daemonId: "d-1",
        state: WorkflowState.STOPPED,
        stopReason: WorkflowStopReason.FAILED,
        activity: ChatActivity.IDLE,
        displayState: RunDisplayState.FAILED,
        createdAtMs: BigInt(NOW),
        completedAtMs: BigInt(NOW + 60_000),
      }),
    );
    expect(run).toMatchObject({
      runId: "wf-1",
      chatId: "chat-1",
      title: "Nightly triage",
      displayState: RunDisplayState.FAILED,
      launchKind: "schedule",
      triggerName: "Nightly",
      createdAt: NOW,
      completedAt: NOW + 60_000,
    });
  });
});
