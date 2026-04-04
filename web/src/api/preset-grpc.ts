// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { singleflight } from "../lib/singleflight";
import { create } from "@bufbuild/protobuf";
import type { PresetInfo as ProtoPresetInfo } from "../gen/reliant/v1/preset_pb";
import { jsToProtoValue, protoValueToJs } from "./proto-utils";
import {
  ListPresetsRequestSchema,
  GetPresetRequestSchema,
  ListPresetsForWorkflowRequestSchema,
  CreatePresetRequestSchema,
  UpdatePresetRequestSchema,
  DeletePresetRequestSchema,
  SetDefaultPresetRequestSchema,
  GetDefaultPresetRequestSchema,
} from "../gen/reliant/v1/preset_pb";

// ============================================
// Frontend Type Definitions
// ============================================

export interface Preset {
  id?: string; // Only present for user presets
  name: string;
  slug?: string; // URL-safe identifier for user presets
  description: string;
  params: Record<string, unknown>;
  source: "builtin" | "project" | "user";
  tag?: string;
  is_hidden?: boolean; // Whether the preset is hidden from the preset picker by default
}

export interface InvalidPreset {
  name: string;
  source: "builtin" | "project" | "user";
  path: string;
  errors: string[];
}

export interface ListPresetsResult {
  presets: Preset[];
  invalidPresets: InvalidPreset[];
}

export interface CreatePresetOptions {
  name: string;
  description?: string;
  params: Record<string, unknown>;
  tag?: string;
}

export interface CreatePresetResult {
  success: boolean;
  error?: string;
  preset?: Preset;
}

export interface UpdatePresetOptions {
  newName?: string;
  newDescription?: string;
  newParams?: Record<string, unknown>;
  newTag?: string;
}

export interface UpdatePresetResult {
  success: boolean;
  error?: string;
  preset?: Preset;
}

export interface DeletePresetResult {
  success: boolean;
  error?: string;
}

export interface SetDefaultPresetResult {
  success: boolean;
  error?: string;
}

// ============================================
// Conversion Functions: Proto -> Frontend
// ============================================

function protoPresetToFrontend(proto: ProtoPresetInfo): Preset {
  // Convert protobuf Value map to plain object
  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(proto.params)) {
    params[key] = protoValueToJs(value as any);
  }

  return {
    id: proto.id || undefined,
    name: proto.name,
    slug: proto.slug || undefined,
    description: proto.description,
    params,
    source: proto.source as "builtin" | "project" | "user",
    tag: proto.tag || undefined,
    is_hidden: proto.isHidden || false,
  };
}

// ============================================
// Preset gRPC Client
// ============================================

export const presetGrpc = {
  /**
   * List all presets for a project
   * @param includeHidden - If true, include items hidden by default (for management UIs)
   * @returns Just the valid presets (for backwards compatibility). Use listPresetsWithErrors for invalid presets.
   */
  async listPresets(projectId: string, includeHidden = false): Promise<Preset[]> {
    const result = await this.listPresetsWithErrors(projectId, includeHidden);
    return result.presets;
  },

  /**
   * List all presets for a project, including invalid presets that failed to load
   * @param includeHidden - If true, include items hidden by default (for management UIs)
   */
  async listPresetsWithErrors(projectId: string, includeHidden = false): Promise<ListPresetsResult> {
    const client = grpcClient.preset();
    const request = create(ListPresetsRequestSchema, { projectId, includeHidden });
    const response = await client.listPresets(request);
    return {
      presets: response.presets.map(protoPresetToFrontend),
      invalidPresets: (response.invalidPresets || []).map(inv => ({
        name: inv.name,
        source: inv.source as "builtin" | "project" | "user",
        path: inv.path,
        errors: [...inv.errors],
      })),
    };
  },

  /**
   * Get a specific preset by name
   */
  async getPreset(projectId: string, name: string): Promise<Preset | null> {
    const client = grpcClient.preset();
    const request = create(GetPresetRequestSchema, { projectId, name });
    const response = await client.getPreset(request);
    if (!response.preset) {
      return null;
    }
    return protoPresetToFrontend(response.preset);
  },

  /**
   * List presets compatible with a specific workflow
   * @param includeHidden - If true, include items hidden by default (for management UIs)
   * @returns Just the valid presets (for backwards compatibility). Use listPresetsForWorkflowWithErrors for invalid presets.
   */
  async listPresetsForWorkflow(projectId: string, workflowName: string, includeHidden = false): Promise<Preset[]> {
    const result = await this.listPresetsForWorkflowWithErrors(projectId, workflowName, includeHidden);
    return result.presets;
  },

  /**
   * List presets compatible with a specific workflow, including invalid presets that failed to load
   * @param includeHidden - If true, include items hidden by default (for management UIs)
   */
  async listPresetsForWorkflowWithErrors(projectId: string, workflowName: string, includeHidden = false): Promise<ListPresetsResult> {
    const client = grpcClient.preset();
    const request = create(ListPresetsForWorkflowRequestSchema, {
      projectId,
      workflowName,
      includeHidden,
    });
    const response = await client.listPresetsForWorkflow(request);
    return {
      presets: response.presets.map(protoPresetToFrontend),
      invalidPresets: (response.invalidPresets || []).map(inv => ({
        name: inv.name,
        source: inv.source as "builtin" | "project" | "user",
        path: inv.path,
        errors: [...inv.errors],
      })),
    };
  },

  /**
   * Create a new preset in the project's .reliant/presets/ directory
   */
  async createPreset(projectId: string, options: CreatePresetOptions): Promise<CreatePresetResult> {
    const client = grpcClient.preset();

    // Convert params to proto Value format
    const protoParams: Record<string, any> = {};
    for (const [key, value] of Object.entries(options.params)) {
      protoParams[key] = jsToProtoValue(value);
    }

    const request = create(CreatePresetRequestSchema, {
      projectId,
      name: options.name,
      description: options.description || "",
      params: protoParams,
      tag: options.tag || "",
    });

    const response = await client.createPreset(request);

    return {
      success: response.success,
      error: response.error || undefined,
      preset: response.preset ? protoPresetToFrontend(response.preset) : undefined,
    };
  },

  /**
   * Update an existing project preset (name, description, or params)
   */
  async updatePreset(
    projectId: string,
    name: string,
    updates: UpdatePresetOptions
  ): Promise<UpdatePresetResult> {
    const client = grpcClient.preset();

    // Convert params to proto Value format if provided
    let protoParams: Record<string, any> | undefined = undefined;
    if (updates.newParams) {
      protoParams = {};
      for (const [key, value] of Object.entries(updates.newParams)) {
        protoParams[key] = jsToProtoValue(value);
      }
    }

    const request = create(UpdatePresetRequestSchema, {
      projectId,
      name,
      newName: updates.newName,
      newDescription: updates.newDescription,
      newParams: protoParams,
      newTag: updates.newTag,
    });

    const response = await client.updatePreset(request);

    return {
      success: response.success,
      error: response.error || undefined,
      preset: response.preset ? protoPresetToFrontend(response.preset) : undefined,
    };
  },

  /**
   * Delete a project preset file
   */
  async deletePreset(projectId: string, name: string): Promise<DeletePresetResult> {
    const client = grpcClient.preset();
    const request = create(DeletePresetRequestSchema, { projectId, name });
    const response = await client.deletePreset(request);

    return {
      success: response.success,
      error: response.error || undefined,
    };
  },

  /**
   * Set a preset as the default for a specific group within a workflow.
   * @param groupName - Group name, or empty string "" for top-level inputs
   */
  async setDefaultPreset(
    projectId: string,
    workflowName: string,
    groupName: string,
    presetName: string | null
  ): Promise<SetDefaultPresetResult> {
    const client = grpcClient.preset();
    const request = create(SetDefaultPresetRequestSchema, {
      projectId,
      workflowName,
      groupName: groupName || undefined,
      presetName: presetName || undefined,
    });
    const response = await client.setDefaultPreset(request);

    return {
      success: response.success,
      error: response.error || undefined,
    };
  },

  /**
   * Get all default presets for a workflow.
   * Returns a map of group name to preset name.
   * Empty string key "" = top-level/workflow-level inputs.
   */
  async getDefaultPresets(
    projectId: string,
    workflowName: string
  ): Promise<Record<string, string>> {
    // Use singleflight to deduplicate concurrent calls with the same args
    // (ChatInput, useWorkflowInputs, and PresetPicker all call this on mount)
    return singleflight(`getDefaultPresets:${projectId}:${workflowName}`, async () => {
      try {
        const client = grpcClient.preset();
        const request = create(GetDefaultPresetRequestSchema, {
          projectId,
          workflowName,
        });
        const response = await client.getDefaultPreset(request);

        // Response.presets is the map from proto
        return response.presets || {};
      } catch (error) {
        // Fail gracefully - return empty map
        console.warn("[preset-grpc] Failed to get default presets:", error);
        return {};
      }
    });
  },
};