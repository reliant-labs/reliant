// Copyright (c) 2025 Reliant Labs

/**
 * The proto → frontend boundary for triggers: the read-only projections the
 * Automations list renders without any other lookup (health, the joined
 * project and machine names, and the launched run's state).
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  ScheduleSourceSchema,
  TriggerEventOutcome,
  TriggerEventRunSchema,
  TriggerEventSchema,
  TriggerHealthSchema,
  TriggerHealthStatus,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import { triggerFromProto } from "../trigger-grpc";

function proto(overrides: Partial<Parameters<typeof create<typeof TriggerSchema>>[1]> = {}) {
  return create(TriggerSchema, {
    id: "trig-1",
    name: "Morning triage",
    projectId: "proj-1",
    daemonId: "daemon-1",
    enabled: true,
    source: { case: "schedule", value: create(ScheduleSourceSchema, { cron: ["0 9 * * *"] }) },
    ...overrides,
  });
}

describe("triggerFromProto", () => {
  it("carries the joined project and machine names", () => {
    const trigger = triggerFromProto(proto({ projectName: "Reliant", daemonName: "MacBook" }));
    expect(trigger.projectName).toBe("Reliant");
    expect(trigger.daemonName).toBe("MacBook");
  });

  it("leaves a deleted project or an unnamed machine unset rather than empty", () => {
    const trigger = triggerFromProto(proto());
    expect(trigger.projectName).toBeUndefined();
    expect(trigger.daemonName).toBeUndefined();
  });

  it.each([
    [TriggerHealthStatus.HEALTHY, "healthy"],
    [TriggerHealthStatus.DEGRADED, "degraded"],
    [TriggerHealthStatus.FAILING, "failing"],
    [TriggerHealthStatus.UNKNOWN, "unknown"],
    [TriggerHealthStatus.UNSPECIFIED, "unknown"],
  ] as const)("maps health %s to %s with its counts", (status, key) => {
    const trigger = triggerFromProto(
      proto({
        health: create(TriggerHealthSchema, {
          status,
          consecutiveFailures: 2,
          consecutiveSkips: 1,
          lastFailureDetail: "model unavailable",
        }),
      }),
    );
    expect(trigger.health).toEqual({
      status: key,
      consecutiveFailures: 2,
      consecutiveSkips: 1,
      lastFailureDetail: "model unavailable",
    });
  });

  it("an older server with no health reads as unknown", () => {
    expect(triggerFromProto(proto()).health.status).toBe("unknown");
  });

  it("carries the launched run's state on the last event", () => {
    const trigger = triggerFromProto(
      proto({
        lastEvent: create(TriggerEventSchema, {
          id: "ev-1",
          outcome: TriggerEventOutcome.LAUNCHED,
          chatId: "chat-1",
          run: create(TriggerEventRunSchema, {
            displayState: RunDisplayState.FAILED,
            title: "Triage",
            state: WorkflowState.STOPPED,
            stopReason: WorkflowStopReason.FAILED,
          }),
        }),
      }),
    );
    expect(trigger.lastEvent?.runDisplayState).toBe(RunDisplayState.FAILED);
  });

  it("a firing that launched nothing has no run", () => {
    const trigger = triggerFromProto(
      proto({ lastEvent: create(TriggerEventSchema, { id: "ev-1", outcome: TriggerEventOutcome.SKIPPED }) }),
    );
    expect(trigger.lastEvent?.runDisplayState).toBeUndefined();
  });
});
