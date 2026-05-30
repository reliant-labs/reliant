// TypeScript workflow types backed by V2 proto schemas.
// Exported names intentionally preserve legacy API surface for callsite stability.

import type { MessageInitShape } from "@bufbuild/protobuf";
import {
  NodeSchema,
  WorkflowSchema,
  InputSchema,
  EdgeSchema,
  EdgeCaseSchema,
  ThreadConfigSchema,
  SaveMessageConfigSchema,
  InjectConfigSchema,
  ProjectConfigSchema,
  PresetsConfigSchema,
  WorkflowUISchema,
  PositionSchema,
  SwitchMetadataSchema,
  SwitchCaseSchema,
} from "../gen/reliant/v1/workflow_v2_pb";
import type {
  RunArgs,
  SubWorkflowArgs,
  LoopArgs,
} from "../gen/reliant/v1/workflow_v2_pb";
import { normalizeCelBoolean, normalizeCelString } from "../lib/celAdapter";
import { TYPE_TO_CASE as ACTION_TYPE_TO_CASE } from "../lib/actionStepArgs";
// =============================================================================
// CORE TYPES - Plain objects derived from V2 proto via MessageInitShape
// =============================================================================

type PresetsConfigProto = MessageInitShape<typeof PresetsConfigSchema>;
type InjectConfigProto = MessageInitShape<typeof InjectConfigSchema>;
type NodeThreadConfigProto = MessageInitShape<typeof ThreadConfigSchema>;
type SaveMessageConfigProto = MessageInitShape<typeof SaveMessageConfigSchema>;
type ProjectConfigProto = MessageInitShape<typeof ProjectConfigSchema>;
type PositionProto = MessageInitShape<typeof PositionSchema>;
type ParamProto = MessageInitShape<typeof InputSchema>;
type StepProto = MessageInitShape<typeof NodeSchema>;
type EdgeCaseProto = MessageInitShape<typeof EdgeCaseSchema>;
type EdgeProto = MessageInitShape<typeof EdgeSchema>;
type SwitchCaseProto = MessageInitShape<typeof SwitchCaseSchema>;
type SwitchMetadataProto = MessageInitShape<typeof SwitchMetadataSchema>;
type WorkflowUIProto = MessageInitShape<typeof WorkflowUISchema>;
type WorkflowProto = MessageInitShape<typeof WorkflowSchema>;

/** Preset matching configuration */
export type PresetsConfig = PresetsConfigProto;

/** Message injection for thread forking */
export type InjectConfig = InjectConfigProto;

/** Node-level thread configuration */
export type NodeThreadConfig = Omit<NodeThreadConfigProto, "memo" | "inject"> & {
  memo?: NodeThreadConfigProto["memo"] | boolean;
  inject?: InjectConfig;
};

/** Auto-save message after node completion */
export type SaveMessageConfig = SaveMessageConfigProto;

/** Project path override for sub-workflows */
export type ProjectConfig = ProjectConfigProto;

/** Position for UI elements */
export type Position = PositionProto;

/**
 * Workflow parameter definition — proto Input init shape.
 * Use helpers from lib/inputHelpers.ts to read/write fields through the config oneof.
 */
export type Param = ParamProto;

/** Step used by workflow builder/editor state.
 *  Structural fields (command, ref, inline, while, presets, project) live inside
 *  the proto args oneof — use the accessor helpers below to read/write them.
 *  Action step params are typed proto args — use helpers from actionStepArgs.ts.
 */
export type Step = Omit<StepProto, "condition" | "saveMessage" | "timeout"> & {
  condition?: StepProto["condition"];
  timeout?: StepProto["timeout"];
  saveMessage?: SaveMessageConfig;
};

/** Edge case for conditional routing */
export type EdgeCase = Omit<EdgeCaseProto, "to" | "condition"> & {
  to?: EdgeCaseProto["to"];
  condition?: EdgeCaseProto["condition"] | string;
  label?: EdgeCaseProto["label"];
};

/** Edge with switch-case routing */
export type Edge = Omit<EdgeProto, "default" | "cases"> & {
  default?: EdgeProto["default"];
  cases?: EdgeCase[];
};

/** Switch case for UI persistence */
export type SwitchCase = SwitchCaseProto;

/** Switch node metadata for UI persistence */
export type SwitchMetadata = Omit<SwitchMetadataProto, "cases"> & {
  cases?: SwitchCase[];
};

/** UI metadata for workflow builder */
export type WorkflowUI = Omit<WorkflowUIProto, "switches"> & {
  switches?: Record<string, SwitchMetadata>;
};

/** Complete workflow definition */
export type Workflow = Omit<WorkflowProto, "nodes" | "edges" | "inputs" | "ui"> & {
  nodes?: Step[];
  edges?: Edge[];
  inputs?: Record<string, Param>;
  ui?: WorkflowUI;
};

// =============================================================================
// STEP TYPE NARROWING ALIASES
// =============================================================================

export type RunStep = Step & { type: "run" };
export type WorkflowStep = Step & { type: "workflow" };
export type JoinStep = Step & { type: "join" };
export type LoopStep = Step & { type: "loop" };
export type RouterStep = Step & { type: "router" };
export type ActionStep = Step;

// =============================================================================
// ADDITIONAL TYPES (frontend-only, not in proto)
// =============================================================================

/** Thread management mode */
export type ThreadMode = "new" | "inherit" | "fork";

/** Thread configuration alias */
export type ThreadConfig = NodeThreadConfig;


/** Response tool definition - custom tools for structured LLM outputs */
export interface ResponseToolDefinition {
  name: string;
  description?: string;
  schema: Record<string, unknown>;
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

const STRUCTURAL_TYPES = new Set(["run", "workflow", "join", "loop", "router"]);

export function isRunStep(step: Step): step is RunStep {
  return step.type === "run";
}

export function isActionStep(step: Step): boolean {
  return step.type !== undefined && !STRUCTURAL_TYPES.has(step.type);
}

export function isWorkflowStep(step: Step): step is WorkflowStep {
  return step.type === "workflow";
}

export function isInlineWorkflow(step: Step): boolean {
  return step.type === "workflow" && getStepInline(step) != null;
}

export function isJoinStep(step: Step): step is JoinStep {
  return step.type === "join";
}

export function isLoopStep(step: Step): step is LoopStep {
  return step.type === "loop";
}

export function isRouterStep(step: Step): step is RouterStep {
  return step.type === "router";
}

export function isInlineLoop(step: Step): boolean {
  return step.type === "loop" && getStepInline(step) != null;
}

export function getStepType(step: Step): string {
  if (!step.type) {
    throw new Error("Step must have a type field");
  }
  return step.type;
}

// =============================================================================
// STEP ARGS ACCESSORS — read from proto args oneof
// =============================================================================

/** Get command string from a run step */
export function getStepCommand(step: Step): string {
  if (step.args?.case === 'run') {
    const runArgs = step.args.value as Partial<RunArgs>
    return normalizeCelString(runArgs?.command)
  }
  return ''
}

/** Get ref string from a workflow or loop step */
export function getStepRef(step: Step): string {
  if (step.args?.case === 'workflow') {
    return normalizeCelString((step.args.value as Partial<SubWorkflowArgs>)?.ref)
  }
  if (step.args?.case === 'loop') {
    return normalizeCelString((step.args.value as Partial<LoopArgs>)?.ref)
  }
  // Fallback: step.type says workflow/loop but args.case doesn't match
  // (can happen with protobuf-es message instances where property access differs from plain objects)
  if ((step.type === 'workflow' || step.type === 'loop') && step.args?.value) {
    const val = step.args.value as Partial<SubWorkflowArgs>
    if (val.ref !== undefined) {
      return normalizeCelString(val.ref)
    }
  }
  return ''
}

/** Get inline workflow from a workflow or loop step */
export function getStepInline(step: Step): Workflow | undefined {
  if (step.args?.case === 'workflow') {
    return (step.args.value as Partial<SubWorkflowArgs>)?.inline as Workflow | undefined
  }
  if (step.args?.case === 'loop') {
    return (step.args.value as Partial<LoopArgs>)?.inline as Workflow | undefined
  }
  if ((step.type === 'workflow' || step.type === 'loop') && step.args?.value) {
    return (step.args.value as Partial<SubWorkflowArgs>)?.inline as Workflow | undefined
  }
  return undefined
}

/** Get while expression string from a loop step */
export function getStepWhile(step: Step): string {
  if (step.args?.case === 'loop') {
    const loopArgs = step.args.value as Partial<LoopArgs>
    const w = loopArgs?.while
    if (w && typeof w === 'object' && 'expr' in w) {
      return (w as { expr: string }).expr
    }
  }
  return ''
}

/** Get parallel flag from a loop step. Returns boolean or CEL expression string. */
export function getStepParallel(step: Step): boolean | string | undefined {
  if (step.args?.case === 'loop') {
    const loopArgs = step.args.value as Partial<LoopArgs>
    if (loopArgs?.parallel !== undefined) {
      return normalizeCelBoolean(loopArgs.parallel, false)
    }
  }
  return undefined
}

/** Get items CEL/string expression from a loop step */
export function getStepItems(step: Step): string {
  if (step.args?.case === 'loop') {
    return normalizeCelString((step.args.value as Partial<LoopArgs>)?.items)
  }
  return ''
}

/** Get iteration key expression from a loop step */
export function getStepKey(step: Step): string {
  if (step.args?.case === 'loop') {
    return normalizeCelString((step.args.value as Partial<LoopArgs>)?.key)
  }
  return ''
}

/** Get on_failure setting from a loop step */
export function getStepOnFailure(step: Step): string {
  if (step.args?.case === 'loop') {
    return normalizeCelString((step.args.value as Partial<LoopArgs>)?.onFailure)
  }
  return ''
}

/** Get the inputs map (args) from a workflow or loop step */
export function getStepInputs(step: Step): Record<string, unknown> {
  if (step.args?.case === 'workflow') {
    return ((step.args.value as Partial<SubWorkflowArgs>)?.args as Record<string, unknown>) ?? {}
  }
  if (step.args?.case === 'loop') {
    return ((step.args.value as Partial<LoopArgs>)?.args as Record<string, unknown>) ?? {}
  }
  if ((step.type === 'workflow' || step.type === 'loop') && step.args?.value) {
    return ((step.args.value as Partial<SubWorkflowArgs>)?.args as Record<string, unknown>) ?? {}
  }
  return {}
}

/** Get presets map from a workflow or loop step */
export function getStepPresets(step: Step): Record<string, string> {
  if (step.args?.case === 'workflow') {
    return (step.args.value as Partial<SubWorkflowArgs>)?.presets ?? {}
  }
  if (step.args?.case === 'loop') {
    return (step.args.value as Partial<LoopArgs>)?.presets ?? {}
  }
  if ((step.type === 'workflow' || step.type === 'loop') && step.args?.value) {
    return (step.args.value as Partial<SubWorkflowArgs>)?.presets ?? {}
  }
  return {}
}

/** Get project config from a workflow or loop step */
export function getStepProject(step: Step): ProjectConfig | undefined {
  if (step.args?.case === 'workflow') {
    return (step.args.value as Partial<SubWorkflowArgs>)?.project as ProjectConfig | undefined
  }
  if (step.args?.case === 'loop') {
    return (step.args.value as Partial<LoopArgs>)?.project as ProjectConfig | undefined
  }
  if (step.args?.case === 'router') {
    return (step.args.value as Record<string, unknown>)?.project as ProjectConfig | undefined
  }
  if ((step.type === 'workflow' || step.type === 'loop') && step.args?.value) {
    return (step.args.value as Partial<SubWorkflowArgs>)?.project as ProjectConfig | undefined
  }
  return undefined
}

/** Get thread config from a workflow or loop step (reads from args oneof).
 *  Returns undefined for any other step shape — callers can't depend on a
 *  thread being present for non-workflow/loop/router steps. */
export function getStepThread(step: Step): NodeThreadConfig | undefined {
  if (step.args?.case === 'workflow') {
    return (step.args.value as Partial<SubWorkflowArgs>)?.thread as NodeThreadConfig | undefined
  }
  if (step.args?.case === 'loop') {
    return (step.args.value as Partial<LoopArgs>)?.thread as NodeThreadConfig | undefined
  }
  if (step.args?.case === 'router') {
    return (step.args.value as Record<string, unknown>)?.thread as NodeThreadConfig | undefined
  }
  return undefined
}

// =============================================================================
// STEP ARGS WRITERS — return new Step with updated args oneof (immutable)
// =============================================================================

/** Update fields on a run step's args. Preserves existing args. */
export function withRunArgs(step: Step, updates: Partial<RunArgs>): Step {
  const current: Partial<RunArgs> = step.args?.case === 'run'
    ? { ...(step.args.value as Partial<RunArgs>) }
    : { env: {} }
  return {
    ...step,
    args: { case: 'run' as const, value: { ...current, ...updates } },
  } as Step
}

/** Update fields on a workflow step's args. Preserves existing args. */
export function withWorkflowArgs(step: Step, updates: Partial<SubWorkflowArgs>): Step {
  const current: Partial<SubWorkflowArgs> = step.args?.case === 'workflow'
    ? { ...(step.args.value as Partial<SubWorkflowArgs>) }
    : { args: {}, presets: {} }
  return {
    ...step,
    args: { case: 'workflow' as const, value: { ...current, ...updates } },
  } as Step
}

/** Update fields on a loop step's args. Preserves existing args. */
export function withLoopArgs(step: Step, updates: Partial<LoopArgs>): Step {
  const current: Partial<LoopArgs> = step.args?.case === 'loop'
    ? { ...(step.args.value as Partial<LoopArgs>) }
    : { args: {}, presets: {} }
  return {
    ...step,
    args: { case: 'loop' as const, value: { ...current, ...updates } },
  } as Step
}

/** Update fields on a router step's args. Preserves existing args. */
export function withRouterArgs(step: Step, updates: Record<string, unknown>): Step {
  const current = step.args?.case === 'router'
    ? { ...(step.args.value as Record<string, unknown>) }
    : { workflows: [] }
  return {
    ...step,
    args: { case: 'router' as const, value: { ...current, ...updates } },
  } as Step
}

/**
 * Merge a step update onto the current step while preserving same-case nested args fields
 * that may be omitted by stale editor snapshots.
 */
export function mergeStepUpdate(currentStep: Step, updatedStep: Step): Step {
  if (
    currentStep.id !== updatedStep.id ||
    currentStep.type !== updatedStep.type ||
    currentStep.args?.case !== updatedStep.args?.case ||
    currentStep.args?.value == null ||
    updatedStep.args?.value == null ||
    typeof currentStep.args.value !== 'object' ||
    typeof updatedStep.args.value !== 'object'
  ) {
    return updatedStep
  }

  return {
    ...currentStep,
    ...updatedStep,
    args: {
      case: updatedStep.args.case,
      value: {
        ...(currentStep.args.value as Record<string, unknown>),
        ...(updatedStep.args.value as Record<string, unknown>),
      },
    },
  } as Step
}

/** Initialize args oneof for a new step based on its type */
export function initStepArgs(type: string): Step['args'] {
  switch (type) {
    case 'run':
      return { case: 'run' as const, value: { env: {} } } as Step['args']
    case 'workflow':
      return { case: 'workflow' as const, value: { args: {}, presets: {} } } as Step['args']
    case 'loop':
      return { case: 'loop' as const, value: { args: {}, presets: {} } } as Step['args']
    case 'router':
      return { case: 'router' as const, value: { workflows: [] } } as Step['args']
    default: {
      // Action step types get their typed args.case from the catalog mapping
      const actionCase = ACTION_TYPE_TO_CASE[type]
      if (actionCase) {
        return { case: actionCase, value: {} } as Step['args']
      }
      return { case: undefined, value: undefined } as Step['args']
    }
  }
}

/** Parse edge source from "node" or "node.event" format */
export function parseEdgeSource(from: string): {
  nodeId: string;
  event?: string;
} {
  const parts = from.split(".");
  if (parts.length === 1) {
    return { nodeId: from };
  }
  return {
    nodeId: parts[0],
    event: parts.slice(1).join("."),
  };
}

function encodeSwitchKey(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, (char) => {
    const hex = char.charCodeAt(0).toString(16).padStart(2, "0");
    return `_x${hex}_`;
  });
}

function normalizeSwitchSource(from: string): string {
  const { nodeId, event } = parseEdgeSource(from);
  if (
    from === "started" ||
    from === "workflow" ||
    nodeId === "started" ||
    nodeId === "workflow"
  ) {
    return "workflow.started";
  }
  return event ? `${nodeId}.${event}` : nodeId;
}

/**
 * Build a stable switch node ID from an edge source string.
 *
 * - "node" -> "switch-node" (legacy-compatible)
 * - "node.event" -> "switch-node_x2e_event" (event-scoped, collision-safe)
 */
export function getSwitchNodeId(from: string): string {
  return `switch-${encodeSwitchKey(normalizeSwitchSource(from))}`;
}