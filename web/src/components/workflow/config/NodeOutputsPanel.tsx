import { useState } from "react";
import { Check, Copy, ChevronDown, ChevronRight, Info } from "lucide-react";
import type { Step } from "../../../types/workflow";
import {
  isActionStep,
  isRunStep,
  isWorkflowStep,
  isLoopStep,
  isJoinStep,
  isRouterStep,
  getStepInline,
  getStepRef,
  getStepParallel,
} from "../../../types/workflow";
import type { NodeInputField } from "../../../gen/reliant/v1/catalog_pb";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface OutputField {
  name: string;
  type: string;
  description: string;
  /** Sub-fields for nested/message types */
  children?: OutputField[];
}

export interface NodeOutputsPanelProps {
  /** The step being configured */
  step: Step;
  /** Output fields from catalog (for action/run nodes) */
  catalogOutputFields?: NodeInputField[];
  /** Resolved outputs from a referenced workflow definition (keys are output names) */
  refOutputs?: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Known nested type definitions
// ---------------------------------------------------------------------------

const NESTED_TYPES: Record<string, OutputField[]> = {
  MessageOutput: [
    { name: "id", type: "string", description: "Message identifier" },
    { name: "role", type: "string", description: "Message role" },
    { name: "text", type: "string", description: "Message text content" },
  ],
  ThinkingOutput: [
    { name: "content", type: "string", description: "Thinking content" },
    { name: "signature", type: "string", description: "Thinking signature" },
  ],
};

// ---------------------------------------------------------------------------
// CopyButton
// ---------------------------------------------------------------------------

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  const handleCopy = () => {
    navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <button
      type="button"
      onClick={handleCopy}
      title={`Copy: ${value}`}
      className="shrink-0 p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
    >
      {copied ? (
        <Check className="w-3 h-3 text-green-500" />
      ) : (
        <Copy className="w-3 h-3" />
      )}
    </button>
  );
}

// ---------------------------------------------------------------------------
// OutputFieldRow (handles nested expansion)
// ---------------------------------------------------------------------------

function OutputFieldRow({
  field,
  celPrefix,
}: {
  field: OutputField;
  celPrefix: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const celPath = `${celPrefix}.${field.name}`;
  const hasChildren = field.children && field.children.length > 0;

  return (
    <>
      <div className="flex items-start gap-3 py-2.5 border-b border-border/50 last:border-0">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            {hasChildren && (
              <button
                type="button"
                onClick={() => setExpanded(!expanded)}
                className="shrink-0 p-0 text-muted-foreground hover:text-foreground transition-colors"
              >
                {expanded ? (
                  <ChevronDown className="w-3 h-3" />
                ) : (
                  <ChevronRight className="w-3 h-3" />
                )}
              </button>
            )}
            <code className="text-xs font-mono text-foreground">
              {field.name}
            </code>
            <span className="text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
              {field.type}
            </span>
          </div>
          {field.description && (
            <p className="text-xs text-muted-foreground mt-0.5 line-clamp-2">
              {field.description}
            </p>
          )}
        </div>
        <CopyButton value={celPath} />
      </div>
      {/* Nested sub-fields */}
      {hasChildren && expanded && (
        <div className="pl-5 border-l border-border/30 ml-2">
          {field.children!.map((child) => (
            <OutputFieldRow
              key={child.name}
              field={child}
              celPrefix={celPath}
            />
          ))}
        </div>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Helpers to build output fields per node type
// ---------------------------------------------------------------------------

function catalogToOutputFields(
  fields: NodeInputField[] | undefined
): OutputField[] {
  if (!fields || fields.length === 0) return [];
  return fields.map((f) => {
    const nested = NESTED_TYPES[f.type];
    return {
      name: f.name,
      type: f.type,
      description: f.description,
      children: nested,
    };
  });
}

function inlineWorkflowOutputFields(step: Step): OutputField[] | null {
  const inline = getStepInline(step);
  if (!inline) return null;

  const outputsMap = inline.outputs;
  if (!outputsMap || Object.keys(outputsMap).length === 0) return [];

  return Object.keys(outputsMap).map((key) => ({
    name: key,
    type: "dynamic",
    description: "",
  }));
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function NodeOutputsPanel({
  step,
  catalogOutputFields,
  refOutputs,
}: NodeOutputsPanelProps) {
  const stepId = step.id || "step_id";

  // Determine output fields and any info banners
  let fields: OutputField[] = [];
  let infoBanner: string | null = null;
  let emptyMessage: string | null = null;

  if (isJoinStep(step)) {
    fields = [
      {
        name: "_sources",
        type: "array",
        description: "Output from each source branch",
      },
    ];
  } else if (isLoopStep(step)) {
    const isParallel = getStepParallel(step) === true || typeof getStepParallel(step) === "string";
    const systemFields: OutputField[] = [
      {
        name: "_iterations",
        type: "integer",
        description: "Total loop iterations completed",
      },
      ...(isParallel
        ? [
            {
              name: "_results",
              type: "map",
              description: "Map of iteration key to sub-workflow outputs for each parallel iteration",
            },
            {
              name: "_completed",
              type: "integer",
              description: "Count of successful parallel iterations",
            },
            {
              name: "_failed",
              type: "integer",
              description: "Count of failed parallel iterations",
            },
            {
              name: "_parallel",
              type: "boolean",
              description: "Whether the loop executed in parallel mode",
            },
          ]
        : []),
    ];

    const inline = getStepInline(step);
    if (inline) {
      // Inline loop
      const inlineFields = inlineWorkflowOutputFields(step);
      if (inlineFields && inlineFields.length > 0) {
        fields = [...inlineFields, ...systemFields];
      } else if (inlineFields && inlineFields.length === 0) {
        emptyMessage =
          "This inline workflow has no declared outputs.";
        fields = systemFields;
      } else {
        fields = systemFields;
      }
    } else {
      // Ref-based loop
      if (refOutputs && Object.keys(refOutputs).length > 0) {
        const refFields = Object.keys(refOutputs).map((key) => ({
          name: key,
          type: "dynamic",
          description: "",
        }));
        fields = [...refFields, ...systemFields];
      } else if (refOutputs !== undefined) {
        // Ref resolved but workflow has no declared outputs
        emptyMessage = "The referenced workflow has no declared outputs.";
        fields = systemFields;
      } else {
        const ref = getStepRef(step);
        if (!ref) {
          infoBanner = "Select a workflow reference to see available outputs.";
        } else {
          infoBanner = "Loading outputs from referenced workflow...";
        }
        fields = systemFields;
      }
    }
  } else if (isWorkflowStep(step)) {
    const inline = getStepInline(step);
    if (inline) {
      // Inline workflow
      const inlineFields = inlineWorkflowOutputFields(step);
      if (inlineFields && inlineFields.length > 0) {
        fields = inlineFields;
      } else {
        emptyMessage =
          "This inline workflow has no declared outputs.";
      }
    } else {
      // Ref-based workflow
      if (refOutputs && Object.keys(refOutputs).length > 0) {
        fields = Object.keys(refOutputs).map((key) => ({
          name: key,
          type: "dynamic",
          description: "",
        }));
      } else if (refOutputs !== undefined) {
        // Ref resolved but workflow has no declared outputs
        emptyMessage = "The referenced workflow has no declared outputs.";
      } else {
        const ref = getStepRef(step);
        if (!ref) {
          infoBanner = "Select a workflow reference to see available outputs.";
        } else {
          infoBanner = "Loading outputs from referenced workflow...";
        }
      }
    }
  } else if (isRouterStep(step)) {
    // Fixed system fields from catalog (or hardcoded fallback)
    const systemFields: OutputField[] = catalogOutputFields
      ? catalogToOutputFields(catalogOutputFields)
      : [
          { name: "selected_workflow", type: "string", description: "The workflow ref selected by the router" },
          { name: "selected_preset", type: "string", description: "The preset selected by the router" },
          { name: "prompt", type: "string", description: "The prompt sent to the selected workflow" },
          { name: "reasoning", type: "string", description: "The router's reasoning for its selection" },
          { name: "outputs", type: "map", description: "Outputs from the selected child workflow" },
        ];

    // User-declared output names from routerArgs.outputs
    const routerOutputs = step.args?.case === "router"
      ? (step.args.value as Record<string, unknown>)?.outputs as Record<string, string> | undefined
      : undefined;
    const declaredFields: OutputField[] = routerOutputs
      ? Object.keys(routerOutputs).map((key) => ({
          name: key,
          type: "dynamic",
          description: "Declared router output",
        }))
      : [];

    fields = [...declaredFields, ...systemFields];
  } else if (isActionStep(step) || isRunStep(step)) {
    fields = catalogToOutputFields(catalogOutputFields);
  }

  return (
    <div className="cpv2-section">
      {/* Header */}
      <div className="text-xs text-muted-foreground mb-3">
        Reference these outputs in downstream nodes using{" "}
        <code className="bg-muted px-1 rounded">nodes.{stepId}.field</code>
      </div>

      {isLoopStep(step) && (getStepParallel(step) === true || typeof getStepParallel(step) === "string") && (
        <div className="cpv2-info-banner mb-3">
          <Info className="w-3.5 h-3.5 shrink-0" />
          <span>
            Parallel loops expose per-iteration outputs via <code className="bg-muted px-1 rounded">nodes.{stepId}._results</code> plus summary fields for completed, failed, and parallel execution state.
          </span>
        </div>
      )}

      {/* Info banner for ref-based workflow/loop */}
      {infoBanner && (
        <div className="cpv2-info-banner mb-3">
          <Info className="w-3.5 h-3.5 shrink-0" />
          <span>{infoBanner}</span>
        </div>
      )}

      {/* Empty message for inline workflows with no outputs */}
      {emptyMessage && fields.length === 0 && (
        <div className="text-center py-6 text-sm text-muted-foreground">
          {emptyMessage}
        </div>
      )}

      {/* Empty message shown above system fields */}
      {emptyMessage && fields.length > 0 && (
        <p className="text-xs text-muted-foreground mb-2 italic">
          {emptyMessage}
        </p>
      )}

      {/* Output field rows */}
      {fields.length > 0 && (
        <div>
          {fields.map((field) => (
            <OutputFieldRow
              key={field.name}
              field={field}
              celPrefix={`nodes.${stepId}`}
            />
          ))}
        </div>
      )}

      {/* Fallback empty state */}
      {fields.length === 0 && !infoBanner && !emptyMessage && (
        <div className="text-center py-6 text-sm text-muted-foreground">
          No output fields available for this node type.
        </div>
      )}
    </div>
  );
}