// Copyright (c) 2025 Reliant Labs
// Workflow gRPC API client - uses proto types directly (no conversion layer)

import { grpcClient } from "./grpc-client";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import type {
  WorkflowListItem as ProtoWorkflowListItem,
  ValidationError,
} from "../gen/reliant/v1/workflow_pb";
import { WorkflowSchema } from "../gen/reliant/v1/workflow_v2_pb";
import type {
  Workflow as PublicWorkflow,
  Step as PublicStep,
  Edge as PublicEdge,
} from "../types/workflow";
import {
  ListWorkflowsRequestSchema,
  CreateWorkflowDraftRequestSchema,
  AssociateChatWithWorkflowDraftRequestSchema,
  GetWorkflowRequestSchema,
  DeleteWorkflowRequestSchema,
  ValidateWorkflowRequestSchema,
  BuilderChatRequestSchema,
  SaveWorkflowRequestSchema,
  ImportWorkflowRequestSchema,
  ExportWorkflowRequestSchema,
  SetWorkflowVisibilityRequestSchema,
  CopyWorkflowRequestSchema,
} from "../gen/reliant/v1/workflow_pb";

type Workflow = PublicWorkflow;
type Step = PublicStep;
type Edge = PublicEdge;

/**
 * Recursively strips $typeName from an object tree.
 *
 * protobuf-es's create(Schema, init) short-circuits when init has a matching
 * $typeName — it returns the object as-is without processing nested fields.
 * When JS spread ({...protoMsg}) copies $typeName but leaves nested objects
 * as plain (no $typeName), serialization fails with "cannot use field X with
 * message undefined". Stripping $typeName forces create() to always take the
 * full recursive initMessage path.
 */
function stripProtoMeta(obj: unknown): unknown {
  if (obj == null || typeof obj !== 'object') return obj;
  if (obj instanceof Uint8Array) return obj;
  if (Array.isArray(obj)) return obj.map(stripProtoMeta);
  const result: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(obj)) {
    if (key === '$typeName' || key === '$unknown') continue;
    result[key] = stripProtoMeta(value);
  }
  return result;
}

function toWorkflowInit(workflow: Workflow): MessageInitShape<typeof WorkflowSchema> {
  // Strip $typeName from the entire object tree so create() always takes the
  // full recursive initMessage path, properly constructing all nested messages.
  const plain = stripProtoMeta(workflow) as Workflow;

  // Normalize edge.default and edge case .to from string|string[] to string[]
  // Proto expects repeated string fields; passing a bare string causes character-by-character iteration.
  const normalizedEdges = (plain.edges || []).map(edge => ({
    ...edge,
    default: edge.default ? (Array.isArray(edge.default) ? edge.default : [edge.default]) : [],
    cases: (edge.cases || []).map(c => ({
      ...c,
      to: c.to ? (Array.isArray(c.to) ? c.to : [c.to]) : [],
    })),
  }));
  return { ...plain, edges: normalizedEdges } as MessageInitShape<typeof WorkflowSchema>;
}

// Re-export workflow types for consumers
export type {
  Workflow,
  Step,
  Edge,
  EdgeCase,
  Param,
  NodeThreadConfig,
  SaveMessageConfig,
  InjectConfig,
  ProjectConfig,
  PresetsConfig,
  WorkflowUI,
  Position,
  SwitchMetadata,
  SwitchCase,
} from "../types/workflow";
export type { ValidationError, HighlightSpan } from "../gen/reliant/v1/workflow_pb";



// ============================================
// API Response Types
// These wrap proto types with additional metadata
// ============================================

/**
 * Workflow response from list operations
 * Extends proto WorkflowListItem with source info
 */
export interface WorkflowResponse {
  name: string;
  filename: string;
  description?: string;
  stepCount: number;
  source: "builtin" | "user" | "project";
  isValid?: boolean;
  nodes: Step[];
  edges: Edge[];
  updatedAt?: string;
  builderChatId?: string;
  isHidden?: boolean;
  hasPresetGroups?: boolean;
  draftId?: string;
}

export interface InvalidWorkflow {
  name: string;
  source: "builtin" | "project" | "user";
  path: string;
  errors: string[];
}

export interface ListWorkflowsResult {
  workflows: WorkflowResponse[];
  invalidWorkflows: InvalidWorkflow[];
}

export interface ToolCallInfo {
  name: string;
  result: string;
}

export interface BuilderChatResponse {
  message: string;
  workflowUpdated: boolean;
  workflow?: Workflow;
  toolCalls: ToolCallInfo[];
}

export interface ValidationResponse {
  valid: boolean;
  errors: ValidationError[];
}

export interface SaveWorkflowResponse {
  success: boolean;
  message: string;
  workflow?: Workflow;
  isValid: boolean;
  validationErrors: ValidationError[];
  id: string;
  slug: string;
  builderChatId?: string;
  version: number;
  yamlDefinition?: string;
}

export interface ImportWorkflowResponse {
  success: boolean;
  message: string;
  workflow?: Workflow;
  id: string;
  slug: string;
  isValid: boolean;
  validationErrors: ValidationError[];
  conflict: boolean;
  existingId: string;
}

export interface ExportWorkflowResponse {
  success: boolean;
  yamlContent: Uint8Array;
  filename: string;
  workflow?: Workflow;
}

// ============================================
// Helper: Convert WorkflowListItem to WorkflowResponse
// ============================================

function listItemToResponse(proto: ProtoWorkflowListItem): WorkflowResponse {
  let source = proto.source || "user";
  if (source === "global" || source === "worktree") {
    source = "user";
  }

  return {
    name: proto.name,
    filename: proto.filename,
    description: proto.description || undefined,
    stepCount: proto.stepCount,
    source: source as WorkflowResponse["source"],
    nodes: proto.nodes,
    edges: proto.edges,
    updatedAt: proto.updatedAt || undefined,
    builderChatId: proto.builderChatId || undefined,
    isValid: proto.isValid !== false,
    isHidden: proto.isHidden || false,
    hasPresetGroups: proto.hasPresetGroups || false,
    draftId: proto.draftId || undefined,
  };
}

// ============================================
// Workflow gRPC Client
// ============================================

export const workflowGrpc = {
  /**
   * List all available workflows for a project
   */
  async listWorkflows(
    projectId: string,
    includeHidden = false,
  ): Promise<WorkflowResponse[]> {
    const result = await this.listWorkflowsWithErrors(projectId, includeHidden);
    return result.workflows;
  },

  /**
   * List all workflows for a project, including invalid workflows
   */
  async listWorkflowsWithErrors(
    projectId: string,
    includeHidden = false,
  ): Promise<ListWorkflowsResult> {
    const client = grpcClient.workflow();
    const request = create(ListWorkflowsRequestSchema, {
      projectId,
      includeHidden,
    });
    const response = await client.listWorkflows(request);
    return {
      workflows: response.workflows.map(listItemToResponse),
      invalidWorkflows: (response.invalidWorkflows || []).map((inv) => ({
        name: inv.name,
        source: inv.source as "builtin" | "project" | "user",
        path: inv.path,
        errors: [...inv.errors],
      })),
    };
  },

  /**
   * Get a specific workflow by name or draft ID
   */
  async getWorkflow(
    projectId: string,
    opts: { name?: string; draftId?: string },
  ): Promise<{
    workflow?: Workflow;
    draftId?: string;
    builderChatId?: string;
    version: number;
    parseError?: string;
    rawDefinition?: string;
    source?: "builtin" | "project" | "user";
    sourcePath?: string;
    yamlDefinition?: string;
  }> {
    const client = grpcClient.workflow();
    const request = create(GetWorkflowRequestSchema, {
      projectId,
      name: opts.name || "",
      draftId: opts.draftId,
    });
    const response = await client.getWorkflow(request);

    let sourceRaw = response.source || "user";
    if (sourceRaw === "global" || sourceRaw === "worktree") {
      sourceRaw = "user";
    }
    const source = sourceRaw as "builtin" | "project" | "user";

    if (response.parseError) {
      return {
        draftId: response.draftId || undefined,
        builderChatId: response.builderChatId || undefined,
        version: Number(response.version),
        parseError: response.parseError,
        rawDefinition: response.rawDefinition || undefined,
        source,
        sourcePath: response.sourcePath || undefined,
        yamlDefinition: response.yamlDefinition || undefined,
      };
    }

    if (!response.workflow) {
      throw new Error("Workflow not found");
    }
    return {
      workflow: response.workflow,
      draftId: response.draftId || undefined,
      builderChatId: response.builderChatId || undefined,
      version: Number(response.version),
      source,
      sourcePath: response.sourcePath || undefined,
      yamlDefinition: response.yamlDefinition || undefined,
    };
  },

  /**
   * Delete a workflow
   */
  async deleteWorkflow(projectId: string, name: string): Promise<void> {
    const client = grpcClient.workflow();
    const request = create(DeleteWorkflowRequestSchema, { projectId, name });
    await client.deleteWorkflow(request);
  },

  /**
   * Validate a workflow without saving it
   */
  async validateWorkflow(
    projectId: string,
    workflow: Workflow,
  ): Promise<ValidationResponse> {
    const client = grpcClient.workflow();
    const request = create(ValidateWorkflowRequestSchema, {
      projectId,
      workflow: create(WorkflowSchema, toWorkflowInit(workflow)),
    });
    const response = await client.validateWorkflow(request);
    return {
      valid: response.valid,
      errors: response.errors,
    };
  },

  /**
   * Send a message to the workflow builder AI assistant
   */
  async builderChat(
    projectId: string,
    sessionId: string,
    message: string,
    workflow: Workflow,
  ): Promise<BuilderChatResponse> {
    const client = grpcClient.workflow();
    const request = create(BuilderChatRequestSchema, {
      projectId,
      sessionId,
      message,
      workflow: create(WorkflowSchema, toWorkflowInit(workflow)),
    });
    const response = await client.builderChat(request);
    return {
      message: response.message,
      workflowUpdated: response.workflowUpdated,
      workflow: response.workflow,
      toolCalls: response.toolCalls.map((tc) => ({
        name: tc.name,
        result: tc.result,
      })),
    };
  },

  /**
   * Save a workflow (creates or updates)
   */
  async saveWorkflow(
    projectId: string,
    workflow: Workflow,
    builderChatId?: string,
    expectedVersion?: number,
    sourcePath?: string,
    draftId?: string,
  ): Promise<SaveWorkflowResponse> {
    const client = grpcClient.workflow();
    const request = create(SaveWorkflowRequestSchema, {
      projectId,
      workflow: create(WorkflowSchema, toWorkflowInit(workflow)),
      builderChatId: builderChatId || undefined,
      expectedVersion: expectedVersion ? BigInt(expectedVersion) : undefined,
      sourcePath: sourcePath || undefined,
      draftId: draftId || undefined,
    });
    const response = await client.saveWorkflow(request);
    return {
      success: response.success,
      message: response.message,
      workflow: response.workflow,
      isValid: response.isValid,
      validationErrors: response.validationErrors,
      id: response.id,
      slug: response.slug,
      builderChatId: response.builderChatId || undefined,
      version: Number(response.version),
      yamlDefinition: response.yamlDefinition || undefined,
    };
  },

  /**
   * Import a workflow from YAML content
   */
  async importWorkflow(
    projectId: string,
    yamlContent: string | Uint8Array,
    overwrite: boolean = false,
  ): Promise<ImportWorkflowResponse> {
    const client = grpcClient.workflow();
    const yamlBytes =
      typeof yamlContent === "string"
        ? new TextEncoder().encode(yamlContent)
        : yamlContent;
    const request = create(ImportWorkflowRequestSchema, {
      projectId,
      yamlContent: yamlBytes,
      overwrite,
    });
    const response = await client.importWorkflow(request);
    return {
      success: response.success,
      message: response.message,
      workflow: response.workflow,
      id: response.id,
      slug: response.slug,
      isValid: response.isValid,
      validationErrors: response.validationErrors,
      conflict: response.conflict,
      existingId: response.existingId,
    };
  },

  /**
   * Export a workflow as YAML
   */
  async exportWorkflow(
    projectId: string,
    slug: string,
    worktreeId?: string,
  ): Promise<ExportWorkflowResponse> {
    const client = grpcClient.workflow();
    const request = create(ExportWorkflowRequestSchema, {
      projectId,
      slug,
      worktreeId,
    });
    const response = await client.exportWorkflow(request);
    return {
      success: response.success,
      yamlContent: response.yamlContent,
      filename: response.filename,
      workflow: response.workflow,
    };
  },

  /**
   * Download a workflow as a YAML file
   */
  async downloadWorkflow(
    projectId: string,
    slug: string,
    worktreeId?: string,
  ): Promise<void> {
    const response = await this.exportWorkflow(projectId, slug, worktreeId);
    if (!response.success) {
      throw new Error("Failed to export workflow");
    }
    const blob = new Blob([new Uint8Array(response.yamlContent)], {
      type: "application/x-yaml",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = response.filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  },

  /**
   * Set workflow visibility (hide/unhide)
   */
  async setWorkflowVisibility(
    projectId: string,
    slug: string,
    isHidden: boolean,
  ): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.workflow();
    const request = create(SetWorkflowVisibilityRequestSchema, {
      projectId,
      slug,
      isHidden,
    });
    const response = await client.setWorkflowVisibility(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  /**
   * Create an empty workflow draft for the builder
   */
  async createWorkflowDraft(
    projectId: string,
  ): Promise<{ draftId: string; slug: string; name: string }> {
    const client = grpcClient.workflow();
    const request = create(CreateWorkflowDraftRequestSchema, {
      projectId,
    });
    const response = await client.createWorkflowDraft(request);
    return {
      draftId: response.draftId,
      slug: response.slug,
      name: response.name,
    };
  },

  /**
   * Associate a chat with a workflow draft
   */
  async associateChatWithWorkflowDraft(
    chatId: string,
    draftId: string,
  ): Promise<void> {
    const client = grpcClient.workflow();
    const request = create(AssociateChatWithWorkflowDraftRequestSchema, {
      chatId,
      draftId,
    });
    await client.associateChatWithWorkflowDraft(request);
  },

  /**
   * Copy a workflow with auto-generated unique name
   */
  async copyWorkflow(
    projectId: string,
    sourceSlug: string,
    newName?: string,
    worktreeId?: string,
  ): Promise<{
    success: boolean;
    message: string;
    slug: string;
    id: string;
  }> {
    const client = grpcClient.workflow();
    const request = create(CopyWorkflowRequestSchema, {
      projectId,
      sourceSlug,
      newName,
      worktreeId,
    });
    const response = await client.copyWorkflow(request);
    return {
      success: response.success,
      message: response.message,
      slug: response.slug,
      id: response.id,
    };
  },
};

// ============================================
// Convenience functions
// These are standalone functions wrapping workflowGrpc methods
// for simpler call-site usage.
// ============================================

/**
 * Get a workflow by name, returning just the Workflow.
 * Throws if the workflow is not found or has a parse error.
 */
export async function getWorkflow(projectId: string, name: string): Promise<Workflow> {
  const result = await workflowGrpc.getWorkflow(projectId, { name })
  if (!result.workflow) {
    throw new Error(result.parseError || "Workflow not found")
  }
  return result.workflow
}

/**
 * Get a workflow with its draft ID, builder chat ID, and version.
 * May return parseError and rawDefinition instead of workflow if stored YAML is invalid.
 */
export async function getWorkflowWithDraftId(projectId: string, name: string) {
  return workflowGrpc.getWorkflow(projectId, { name })
}

/**
 * Get a workflow by its draft ID directly.
 * May return parseError and rawDefinition instead of workflow if stored YAML is invalid.
 */
export async function getWorkflowByDraftId(projectId: string, draftId: string) {
  return workflowGrpc.getWorkflow(projectId, { draftId })
}
