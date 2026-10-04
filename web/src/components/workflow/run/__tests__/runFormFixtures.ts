// Copyright (c) 2025 Reliant Labs

/**
 * Proto fixtures for the run-form tests: a workflow definition with typed
 * inputs, presets and worktrees, built as the RPCs return them so the real
 * client-side mapping runs.
 *
 * The "triage" workflow declares:
 *   - depth     integer, default 2           (optional)
 *   - label     string, NO default           (required)
 *   - review    group, preset tag "review":
 *       review.strictness  enum low|high, default "low"
 * and its workflow-level preset tag is "agent".
 */

import { create } from "@bufbuild/protobuf";
import {
  EnumInputConfigSchema,
  GroupInputConfigSchema,
  InputBaseSchema,
  InputSchema,
  IntegerInputConfigSchema,
  PresetsConfigSchema,
  StringInputConfigSchema,
  WorkflowSchema,
  type Input,
} from "@/gen/reliant/v1/workflow_v2_pb";
import { GetWorkflowResponseSchema } from "@/gen/reliant/v1/workflow_pb";
import { ListPresetsForWorkflowResponseSchema, PresetInfoSchema } from "@/gen/reliant/v1/preset_pb";
import { ListWorktreesResponseSchema, WorktreeSchema } from "@/gen/reliant/v1/worktree_pb";
import { jsToProtoValue } from "@/api/proto-utils";

function integerInput(description: string, fallback?: number): Input {
  return create(InputSchema, {
    type: "integer",
    config: {
      case: "integerInput",
      value: create(IntegerInputConfigSchema, {
        base: create(InputBaseSchema, { description }),
        default: fallback === undefined ? undefined : BigInt(fallback),
      }),
    },
  });
}

function stringInput(description: string, fallback?: string): Input {
  return create(InputSchema, {
    type: "string",
    config: {
      case: "stringInput",
      value: create(StringInputConfigSchema, {
        base: create(InputBaseSchema, { description }),
        default: fallback,
      }),
    },
  });
}

function enumInput(values: string[], fallback: string): Input {
  return create(InputSchema, {
    type: "enum",
    config: {
      case: "enumInput",
      value: create(EnumInputConfigSchema, {
        base: create(InputBaseSchema, {}),
        enumValues: values,
        default: jsToProtoValue(fallback),
      }),
    },
  });
}

export function triageWorkflowResponse() {
  return create(GetWorkflowResponseSchema, {
    source: "project",
    workflow: create(WorkflowSchema, {
      name: "triage",
      presets: create(PresetsConfigSchema, { tag: "agent" }),
      inputs: {
        depth: integerInput("How deep to look", 2),
        label: stringInput("Issue label to triage"),
        review: create(InputSchema, {
          type: "group",
          config: {
            case: "groupInput",
            value: create(GroupInputConfigSchema, {
              base: create(InputBaseSchema, {}),
              presets: create(PresetsConfigSchema, { tag: "review" }),
              inputs: { strictness: enumInput(["low", "high"], "low") },
            }),
          },
        }),
      },
    }),
  });
}

/** A second workflow with one optional input, for workflow switches. */
export function sweepWorkflowResponse() {
  return create(GetWorkflowResponseSchema, {
    source: "project",
    workflow: create(WorkflowSchema, {
      name: "sweep",
      inputs: { days: integerInput("Age in days", 30) },
    }),
  });
}

export function presetsResponse() {
  return create(ListPresetsForWorkflowResponseSchema, {
    presets: [
      create(PresetInfoSchema, {
        name: "careful",
        description: "Look harder",
        source: "project",
        tag: "agent",
        params: { depth: jsToProtoValue(5), label: jsToProtoValue("bug") },
      }),
      create(PresetInfoSchema, {
        name: "strict",
        source: "project",
        tag: "review",
        params: { strictness: jsToProtoValue("high") },
      }),
    ],
  });
}

export function worktreesResponse(projectId: string) {
  return create(ListWorktreesResponseSchema, {
    worktrees: [
      create(WorktreeSchema, { id: "wt-main", name: "main", branch: "main", projectId, isMain: true }),
      create(WorktreeSchema, { id: "wt-1", name: "feature", branch: "feat/x", projectId }),
      create(WorktreeSchema, { id: "wt-2", name: "hotfix", branch: "fix/y", projectId }),
    ],
    total: 3,
  });
}

/** listWorkflows payload naming both workflows. */
export const WORKFLOW_LIST = {
  workflows: [
    { name: "agent", source: "builtin", stepCount: 1, nodes: [], edges: [], validationErrors: [] },
    { name: "triage", source: "project", stepCount: 2, nodes: [], edges: [], validationErrors: [] },
    { name: "sweep", source: "project", stepCount: 1, nodes: [], edges: [], validationErrors: [] },
  ],
  invalidWorkflows: [],
};

/** getWorkflow resolved by name. */
export function getWorkflowByName(request: { name: string }) {
  if (request.name === "triage") return triageWorkflowResponse();
  if (request.name === "sweep") return sweepWorkflowResponse();
  return create(GetWorkflowResponseSchema, {
    source: "builtin",
    workflow: create(WorkflowSchema, { name: request.name, inputs: {} }),
  });
}
