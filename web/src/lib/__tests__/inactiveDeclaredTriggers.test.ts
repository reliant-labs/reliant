/**
 * Declared-but-inactive triggers: what a workflow's author meant to run and
 * nothing activates yet. "Inactive" is per caller and across every project:
 * one activation anywhere makes the declaration active.
 */

import { describe, expect, it } from "vitest";

import type { Trigger } from "../../api/trigger-grpc";
import type { DeclaredTrigger } from "../declaredTriggers";
import { inactiveDeclaredTriggers } from "../triggerRail";

const nightly = { name: "nightly", source: { case: "schedule", value: { cron: ["0 9 * * 1-5"] } } } as unknown as DeclaredTrigger;
const hook = { name: "ci-deploy", source: { case: "webhook", value: {} } } as unknown as DeclaredTrigger;

function activation(workflow: string, workflowTrigger: string, projectId = "proj-2"): Trigger {
  return {
    id: `${workflow}/${workflowTrigger}`,
    name: workflowTrigger,
    projectId,
    enabled: true,
    workflow,
    workflowTrigger,
    health: { status: "healthy", consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "" },
    source: { kind: "activation", workflowTrigger },
  } as unknown as Trigger;
}

describe("inactiveDeclaredTriggers", () => {
  it("lists each declaration nothing of the caller's activates, in any project", () => {
    const items = inactiveDeclaredTriggers(
      [
        { name: "triage", title: "Triage", triggers: [nightly, hook] },
        { name: "builtin://agent", triggers: [] },
        { name: "release", triggers: [hook] },
      ],
      [activation("triage", "nightly"), activation("release", "nightly")],
    );
    expect(items.map((i) => `${i.workflowRef}:${i.declared.name}`)).toEqual(["triage:ci-deploy", "release:ci-deploy"]);
    expect(items[0]).toMatchObject({ workflowTitle: "Triage" });
    expect(items[1]).toMatchObject({ workflowTitle: "release" });
  });

  it("matches activations by ref without its scheme, and lists a workflow once", () => {
    const items = inactiveDeclaredTriggers(
      [
        { name: "builtin://digest", triggers: [nightly] },
        { name: "builtin://digest", triggers: [nightly] },
      ],
      [],
    );
    expect(items).toHaveLength(1);
    expect(inactiveDeclaredTriggers([{ name: "builtin://digest", triggers: [nightly] }], [activation("digest", "nightly")])).toEqual([]);
  });
});
