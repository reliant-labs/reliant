import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { InboxAutomationFailingSchema } from "@/gen/reliant/v1/inbox_pb";
import { automationFailureDetail } from "../InboxItem";

describe("attention failure copy", () => {
  it.each([
    ["unsuccessful", "Job unsuccessful"],
    ["execution_error", "Execution failed"],
    ["recovery_expired", "Recovery timed out"],
  ])("names %s independently of diagnostic health", (failureClass, label) => {
    const failure = create(InboxAutomationFailingSchema, {
      incidentKey: "repo:acme:pr:42",
      attentionState: "action_required",
      failureClass,
      detail: "Repair needs a person",
      health: { consecutiveFailures: 12, lastFailureDetail: "an unrelated trigger failure" },
    });
    expect(automationFailureDetail(failure)).toBe(`${label} · Repair needs a person`);
    failure.detail = "";
    expect(automationFailureDetail(failure)).toBe(label);
  });

  it.each(["", "future_class"])("keeps legacy copy for class %j", (failureClass) => {
    const failure = create(InboxAutomationFailingSchema, {
      failureClass,
      health: { consecutiveFailures: 3, lastFailureDetail: "daemon disconnected" },
    });
    expect(automationFailureDetail(failure)).toBe("3 in a row · daemon disconnected");
    failure.health!.consecutiveFailures = 1;
    expect(automationFailureDetail(failure)).toBe("daemon disconnected");
    expect(automationFailureDetail(create(InboxAutomationFailingSchema))).toBe("");
  });
});
