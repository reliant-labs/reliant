// Copyright (c) 2025 Reliant Labs

/**
 * Load one workflow's definition, in an explicit project, and derive its
 * input groups.
 *
 * Store-free on purpose: the project is a parameter, never the current one.
 * The builder's step config and the Run… / automation forms all need "the
 * inputs of workflow W in project P", and only some of them are looking at P.
 */

import { useEffect, useMemo, useState } from "react";
import { workflowGrpc, type Workflow } from "../../api/workflow-grpc";
import type { InputGroupDef } from "./WorkflowInputGroup";
import type { InputDef } from "../../lib/inputHelpers";
import {
  getInputNestedInputs,
  getInputPresetConfig,
  getInputUI,
  isConfigurableInput,
} from "../../lib/inputHelpers";
import { canonicalizeBuiltinWorkflowRef } from "./workflowRef";

export interface UseWorkflowDefinitionResult {
  /** Parsed definition; null while loading, on error, or with no workflow. */
  workflowDef: Workflow | null;
  loading: boolean;
  /** Why the definition is unusable: a fetch failure or a parse error. */
  error: string | null;
  /** Configurable inputs, grouped as the input renderers expect. */
  inputGroups: InputGroupDef[];
}

/** Build input groups from a workflow definition by iterating proto inputs directly. */
export function buildInputGroups(workflowDef: Workflow | null): InputGroupDef[] {
  if (!workflowDef) return [];

  const rawInputs = (workflowDef.inputs ?? (workflowDef as any).params) as Record<string, any> | undefined;
  if (!rawInputs || Object.keys(rawInputs).length === 0) return [];

  const groups: InputGroupDef[] = [];
  const topLevel: Array<{ name: string; schema: InputDef }> = [];
  const groupedMap = new Map<string, { presets?: { tag: string }; ui?: string; inputs: Array<{ name: string; schema: InputDef }> }>();

  for (const [name, rawInput] of Object.entries(rawInputs)) {
    if (rawInput?.type === "group") {
      const nestedInputs = getInputNestedInputs(rawInput);
      const presetConfig = getInputPresetConfig(rawInput);
      const ui = getInputUI(rawInput);
      const group: { presets?: { tag: string }; ui?: string; inputs: Array<{ name: string; schema: InputDef }> } = {
        presets: presetConfig?.tag ? { tag: presetConfig.tag } : undefined,
        ui,
        inputs: [],
      };
      if (nestedInputs) {
        for (const [paramName, nestedRaw] of Object.entries(nestedInputs)) {
          if (!isConfigurableInput(nestedRaw)) continue;
          group.inputs.push({ name: `${name}.${paramName}`, schema: nestedRaw });
        }
      }
      if (group.inputs.length > 0) {
        groupedMap.set(name, group);
      }
    } else {
      if (!isConfigurableInput(rawInput)) continue;
      topLevel.push({ name, schema: rawInput });
    }
  }

  if (topLevel.length > 0) {
    groups.push({
      name: "",
      label: "Parameters",
      presets: workflowDef.presets,
      inputs: topLevel,
    });
  }

  for (const [groupName, groupData] of groupedMap) {
    groups.push({
      name: groupName,
      label: groupName,
      presets: groupData.presets,
      inputs: groupData.inputs,
    });
  }

  return groups;
}

export function useWorkflowDefinition(
  projectId: string | undefined,
  workflowRef: string,
  enabled = true,
): UseWorkflowDefinitionResult {
  const [workflowDef, setWorkflowDef] = useState<Workflow | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!projectId || !workflowRef || !enabled) {
      setWorkflowDef(null);
      setError(null);
      return;
    }

    // A slow response for a workflow the caller has since moved off must not
    // land on top of the current one.
    let cancelled = false;
    const fetchDef = async () => {
      setLoading(true);
      try {
        const builtinWorkflowRefs = (await workflowGrpc.listWorkflows(projectId))
          .filter((workflow) => workflow.source === "builtin")
          .map((workflow) => workflow.name);
        const canonicalWorkflowRef = canonicalizeBuiltinWorkflowRef(
          workflowRef,
          builtinWorkflowRefs,
        );
        const result = await workflowGrpc.getWorkflow(projectId, {
          name: canonicalWorkflowRef,
        });
        if (cancelled) return;
        setWorkflowDef(result.workflow ?? null);
        setError(result.parseError ?? null);
      } catch (fetchError) {
        if (cancelled) return;
        console.error("Failed to fetch workflow definition:", fetchError);
        setWorkflowDef(null);
        setError(fetchError instanceof Error ? fetchError.message : String(fetchError));
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    void fetchDef();
    return () => {
      cancelled = true;
    };
  }, [projectId, workflowRef, enabled]);

  const inputGroups = useMemo(() => buildInputGroups(workflowDef), [workflowDef]);

  return { workflowDef, loading, error, inputGroups };
}
