/**
 * Guard: the builder's workflow path never serializes, copies, keys or
 * compares a workflow in a way that throws on a bigint.
 *
 * protobuf-es decodes every int64 as a bigint — an integer input's default,
 * min and max, CelInt literals such as call_llm's max_tokens — and
 * JSON.stringify throws on one ("Do not know how to serialize a BigInt").
 * Live validation keyed the canvas with JSON.stringify and crashed on any
 * workflow with an integer input that had a default; undo history and the
 * viewer's node-data compare did the same on a step with a CelInt literal.
 *
 * Two halves:
 *   - every such path runs on a fixture with EVERY int64 field the Workflow
 *     proto can reach set to a bigint, built from the descriptors, so a new
 *     int64 field is covered the day it is added;
 *   - the files on the path may not call JSON.stringify, except at the sites
 *     listed below with the reason each cannot see a bigint. A new call fails
 *     here: use toJsonString with the schema (lib/workflowProto), copy with
 *     structuredClone, compare with structurallyEqual.
 */
import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { Node } from "@xyflow/react";
import { equals, fromJsonString, toJsonString } from "@bufbuild/protobuf";

import { WorkflowSchema } from "../../gen/reliant/v1/workflow_v2_pb";
import { WorkflowTriggerSchema } from "../../gen/reliant/v1/trigger_pb";
import { BIG_INT64, everyInt64Workflow, int64FieldNames, int64FieldsSetIn } from "../../test/int64Workflow";
import { validationKey } from "../../components/workflow/hooks/useLiveValidation";
import { useUndoRedo } from "../../hooks/useUndoRedo";
import { workflowToFlowElements } from "../workflow-flow";
import { nodesEdgesToWorkflow } from "../nodes-edges-to-workflow";
import { structurallyEqual } from "../structuralEqual";
import { toWorkflowMessage } from "../workflowProto";
import type { Workflow } from "../../types/workflow";

const SRC = join(__dirname, "..", "..");

/** Swap one bigint for another in a copy: an edit only an int64 tells apart. */
function bumpedInt64(workflow: Workflow): Workflow {
  const copy = structuredClone(workflow);
  const input = Object.values(copy.inputs ?? {}).find((i) => i.config?.case === "integerInput")!;
  (input.config!.value as { default?: bigint }).default = BIG_INT64 + 1n;
  return copy;
}

describe("the every-int64 fixture", () => {
  it("sets every int64 field a Workflow can reach, as a bigint", () => {
    const fixture = everyInt64Workflow();
    const reachable = int64FieldNames(WorkflowSchema);
    expect(reachable.size).toBeGreaterThan(0);
    expect(int64FieldsSetIn(WorkflowSchema, fixture)).toEqual(reachable);
  });
});

describe("the workflow path is bigint-safe", () => {
  it("live validation keys it, and an int64-only edit changes the key", () => {
    const fixture = everyInt64Workflow();
    const key = validationKey(fixture);
    expect(key).toContain(`"${BIG_INT64}"`);
    expect(validationKey(structuredClone(fixture))).toBe(key);
    expect(validationKey(bumpedInt64(fixture))).not.toBe(key);
  });

  it("a save's message round-trips it through proto JSON exactly", () => {
    const message = toWorkflowMessage(everyInt64Workflow());
    const reloaded = fromJsonString(WorkflowSchema, toJsonString(WorkflowSchema, message));
    expect(equals(WorkflowSchema, reloaded, message)).toBe(true);
  });

  it("the canvas copies, compares and saves it: undo history, node data, back to a workflow", () => {
    const fixture = everyInt64Workflow();
    const { nodes, edges } = workflowToFlowElements(fixture);
    const { result } = renderHook(() => useUndoRedo());
    act(() => result.current.takeSnapshot(nodes, edges));
    let undone: { nodes: Node[] } | null = null;
    act(() => {
      undone = result.current.undo(nodes, edges);
    });
    expect(structurallyEqual(undone!.nodes, nodes)).toBe(true);

    // The viewer's "did this node's data change" check, on data holding bigints.
    const data = nodes.map((n) => n.data);
    const edited = structuredClone(data);
    const literal = (edited.find((d) => (d.step as { type?: string } | undefined)?.type === "call_llm")!.step as {
      args: { value: { maxTokens: { value: { value: bigint } } } };
    }).args.value.maxTokens.value;
    expect(literal.value).toBe(BIG_INT64);
    expect(structurallyEqual(data, structuredClone(data))).toBe(true);
    literal.value = BIG_INT64 + 1n;
    expect(structurallyEqual(data, edited)).toBe(false);

    const saved = nodesEdgesToWorkflow(undone!.nodes, edges, {
      name: fixture.name ?? "",
      description: "",
      inputs: fixture.inputs ?? {},
      outputs: {},
      entry: fixture.entry,
      tag: undefined,
      presetDefault: undefined,
      apiVersion: undefined,
      isLocked: false,
      definition: fixture,
    });
    const json = toJsonString(WorkflowSchema, toWorkflowMessage(saved));
    expect(int64FieldsSetIn(WorkflowSchema, fromJsonString(WorkflowSchema, json))).toEqual(int64FieldNames(WorkflowSchema));
  });

  it("structurallyEqual tells bigints apart by value", () => {
    expect(structurallyEqual({ n: 1n, s: "x" }, { s: "x", n: 1n })).toBe(true);
    expect(structurallyEqual({ n: 1n }, { n: 2n })).toBe(false);
    expect(structurallyEqual({ n: 1n }, { n: 1 })).toBe(false);
    expect(structurallyEqual({ a: undefined, b: [1n] }, { b: [1n] })).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// No new JSON.stringify on the path
// ---------------------------------------------------------------------------

/** Files that hold workflow, step or input protos. */
const GUARDED_FILES = [
  "api/workflow-grpc.ts",
  "lib/workflowProto.ts",
  "lib/inputHelpers.ts",
  "lib/workflow-flow.ts",
  "lib/nodes-edges-to-workflow.ts",
  "hooks/useUndoRedo.ts",
  "components/workflow/WorkflowBuilder.tsx",
  "components/workflow/WorkflowViewer.tsx",
  "components/workflow/WorkflowParamsEditor.tsx",
  "components/workflow/WorkflowSettingsEditor.tsx",
  "components/workflow/WorkflowMutationContext.tsx",
  ...readdirSync(join(SRC, "components/workflow/hooks"))
    .filter((name) => /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name))
    .map((name) => `components/workflow/hooks/${name}`),
];

const TRIGGERS = "WorkflowTrigger reaches no int64 field (asserted below)";

/** "file: JSON.stringify(args)" → why it cannot see a bigint. */
const ALLOWED: Record<string, string> = {
  "components/workflow/WorkflowBuilder.tsx: JSON.stringify(builtWorkflow.triggers ?? [])": TRIGGERS,
  "components/workflow/WorkflowBuilder.tsx: JSON.stringify(initialWorkflow?.triggers ?? [])": TRIGGERS,
  "components/workflow/WorkflowBuilder.tsx: JSON.stringify(t)": TRIGGERS,
  "components/workflow/WorkflowBuilder.tsx: JSON.stringify(currentNodeIds)": "sorted string ids",
  "components/workflow/WorkflowBuilder.tsx: JSON.stringify(incomingNodeIds)": "sorted string ids",
  "components/workflow/WorkflowParamsEditor.tsx: JSON.stringify(defaultVal, null, 2)":
    "an object input's default: a google.protobuf.Value read back as JSON values",
};

/** Every `JSON.stringify(...)` call in `source`, with its full argument text. */
function stringifyCalls(source: string): string[] {
  const calls: string[] = [];
  const marker = "JSON.stringify(";
  for (let at = source.indexOf(marker); at !== -1; at = source.indexOf(marker, at + 1)) {
    let depth = 0;
    let end = at + marker.length - 1;
    for (; end < source.length; end++) {
      if (source[end] === "(") depth++;
      else if (source[end] === ")" && --depth === 0) break;
    }
    calls.push(source.slice(at, end + 1).replace(/\s+/g, " "));
  }
  return calls;
}

describe("no bigint-unsafe JSON.stringify on the workflow path", () => {
  it("only the listed sites call it", () => {
    const unexpected: string[] = [];
    const seen = new Set<string>();
    for (const file of GUARDED_FILES) {
      for (const call of stringifyCalls(readFileSync(join(SRC, file), "utf8"))) {
        const site = `${file}: ${call}`;
        seen.add(site);
        if (!(site in ALLOWED)) unexpected.push(site);
      }
    }
    expect(
      unexpected,
      "A workflow, step or input proto holds int64 fields as bigints, which JSON.stringify throws on. " +
        "Serialize with toJsonString(Schema, toWorkflowMessage(...)), copy with structuredClone, compare with " +
        "structurallyEqual — or, if this call can never see a bigint, add it to ALLOWED with the reason.",
    ).toEqual([]);
    // A stale entry would quietly allow a future call with the same text.
    expect(Object.keys(ALLOWED).filter((site) => !seen.has(site))).toEqual([]);
  });

  it("covers files that exist", () => {
    for (const file of GUARDED_FILES) expect(() => readFileSync(join(SRC, file)), relative(SRC, join(SRC, file))).not.toThrow();
  });

  it("the triggers' allowance holds: a WorkflowTrigger reaches no int64 field", () => {
    expect([...int64FieldNames(WorkflowTriggerSchema)]).toEqual([]);
  });
});
