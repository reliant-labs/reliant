import { Edit3, Eye } from "lucide-react";
import { Eye, Edit3 } from "lucide-react";
import type { ProtoFieldSchema } from "../../../types/workflowFieldSchema";
import type { LoopStep } from "../../../types/workflow";
import {
  getStepInline,
  getStepWhile,
  getStepParallel,
  getStepItems,
  getStepKey,
  getStepOnFailure,
  getStepInputs,
  withLoopArgs,
} from "../../../types/workflow";
import { CELExpressionInput } from "../CELInput";
import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { inputDefToSchema } from "../../../lib/nodeFieldAdapter";
import { getInputUI } from "../../../lib/inputHelpers";
import { directCel, celString } from "../../../lib/celAdapter";

/**
 * Editor for inline loop inputs - displays inputs defined in the inline workflow
 * and allows users to set values that get passed as args.
 * Uses ProtoFieldRenderer for consistent rendering with action steps.
 */
export function InlineLoopInputsEditor({
  inputs,
  values,
  onChange,
}: {
  inputs: Record<string, any>;
  values: Record<string, unknown>;
  onChange: (values: Record<string, unknown>) => void;
}) {
  const inputEntries = Object.entries(inputs).filter(
    ([_, param]) => getInputUI(param) !== "hidden",
  );

  if (inputEntries.length === 0) {
    return null;
  }

  const handleInputChange = (name: string, value: unknown) => {
    onChange({ ...values, [name]: value });
  };

  return (
    <div className="space-y-3 border-t border-border pt-3">
      <label className="block text-sm font-medium text-foreground">
        Loop Inputs
      </label>
      <div className="space-y-2">
        {inputEntries.map(([name, param]) => {
          if (!param.type) return null;
          return (
            <ProtoFieldRenderer
              key={name}
              schema={inputDefToSchema(name, param)}
              value={values[name]}
              onChange={(value) => handleInputChange(name, value)}
            />
          );
        })}
      </div>
    </div>
  );
}

const PARALLEL_FIELD_SCHEMA: ProtoFieldSchema = {
  key: "parallel",
  label: "Run iterations in parallel",
  widget: "checkbox",
  valueKind: "boolean",
  celCapable: true,
  defaultValue: false,
  omitIfDefault: true,
  helpText:
    "Run all iterations concurrently. Parallel loops require Items and use per-iteration outputs in _results.",
};

const ON_FAILURE_SCHEMA: ProtoFieldSchema = {
  key: "on_failure",
  label: "On Failure",
  widget: "select",
  valueKind: "string",
  defaultValue: "continue",
  omitIfDefault: true,
  options: [
    { value: "continue", label: "continue — keep other iterations running" },
    { value: "fail_fast", label: "fail_fast — cancel remaining iterations" },
    { value: "fail_all", label: "fail_all — wait for all, then fail if any failed" },
  ],
  helpText: "Controls how parallel loops behave when an iteration fails.",
};

export function LoopStepConfig({
  step,
  onUpdate,
  onEditLoopBody,
  isReadOnly = false,
}: {
  step: LoopStep;
  onUpdate: (step: LoopStep) => void;
  onEditLoopBody?: (step: LoopStep) => void;
  isReadOnly?: boolean;
}) {
  const stepInputs = getStepInputs(step);
  const parallelValue = getStepParallel(step);
  const itemsValue = getStepItems(step);
  const keyValue = getStepKey(step);
  const onFailureValue = getStepOnFailure(step) || "continue";
  const isParallel = parallelValue === true || (typeof parallelValue === "string" && parallelValue.length > 0);

  // Initialize inline workflow if it doesn't exist
  const inlineWorkflow = getStepInline(step);
  if (!inlineWorkflow) {
    onUpdate(
      withLoopArgs(step, {
        inline: {
          name: "",
          entry: [],
          nodes: [],
          edges: [],
          outputs: {},
        } as any,
      }) as LoopStep,
    );
  }

  return (
    <>
      {/* Loop Body */}
      <div>
        <label className="text-xs uppercase tracking-wider text-muted-foreground font-medium mb-2 block">
          Loop Body
        </label>
        <button
          onClick={() => onEditLoopBody?.(step)}
          disabled={!onEditLoopBody}
          className="w-full flex items-center justify-between px-3 py-2 border border-input rounded-md bg-muted/30 hover:bg-muted transition-colors disabled:opacity-50 disabled:cursor-not-allowed text-sm"
        >
          <span className="text-muted-foreground">
            {inlineWorkflow?.nodes?.length || 0} nodes ·{" "}
            {inlineWorkflow?.edges?.length || 0} edges
          </span>
          <span className="flex items-center gap-1 text-primary">
            {isReadOnly ? (
              <Eye className="w-3.5 h-3.5" />
            ) : (
              <Edit3 className="w-3.5 h-3.5" />
            )}
            {isReadOnly ? "View" : "Edit"}
          </span>
        </button>
      </div>

      {/* Inline loop inputs - show params defined in the inline workflow */}
      {inlineWorkflow?.inputs &&
        Object.keys(inlineWorkflow.inputs).length > 0 && (
          <InlineLoopInputsEditor
            inputs={inlineWorkflow.inputs}
            values={stepInputs}
            onChange={(inputs) =>
              onUpdate(withLoopArgs(step, { args: inputs as any }) as LoopStep)
            }
          />
        )}

      <ProtoFieldRenderer
        schema={PARALLEL_FIELD_SCHEMA}
        value={parallelValue}
        onChange={(value) =>
          onUpdate(
            withLoopArgs(step, {
              parallel:
                value === undefined || value === false || value === ""
                  ? undefined
                  : typeof value === "string"
                    ? celString(value) as any
                    : Boolean(value),
            }) as LoopStep,
          )
        }
        disabled={isReadOnly}
        celContext="thread"
      />

      <CELExpressionInput
        label="Items"
        helpTooltip="CEL expression that evaluates to a list or map of iteration items. Required for parallel loops, optional for sequential loops."
        value={itemsValue}
        onChange={(val) =>
          onUpdate(
            withLoopArgs(step, {
              items: val ? celString(val) as any : undefined,
            }) as LoopStep,
          )
        }
        placeholder="nodes.decompose.output.components"
        celContext="workflow"
        disabled={isReadOnly}
        hideCELHint
        showCELIndicator={false}
      />

      {isParallel && (
        <CELExpressionInput
          label="Key (optional)"
          helpTooltip="CEL expression used to key each iteration in nodes.<loop>._results. Defaults to the iteration index."
          value={keyValue}
          onChange={(val) =>
            onUpdate(
              withLoopArgs(step, {
                key: val || undefined,
              }) as LoopStep,
            )
          }
          placeholder="{{iter.item.name}}"
          celContext="workflow"
          disabled={isReadOnly}
          hideCELHint
          showCELIndicator={false}
        />
      )}

      {isParallel && (
        <ProtoFieldRenderer
          schema={ON_FAILURE_SCHEMA}
          value={onFailureValue}
          onChange={(value) =>
            onUpdate(
              withLoopArgs(step, {
                onFailure: typeof value === "string" ? value : undefined,
              }) as LoopStep,
            )
          }
          disabled={isReadOnly}
          celContext="workflow"
        />
      )}

      {!isParallel && (
        <CELExpressionInput
          label="While (optional)"
          helpTooltip="CEL expression that ends the loop when true. Access iteration outputs via 'outputs' namespace."
          value={getStepWhile(step)}
          onChange={(val) =>
            onUpdate(
              withLoopArgs(step, {
                while: val ? directCel(val) as any : undefined,
              }) as LoopStep,
            )
          }
          placeholder="outputs.done == true"
          celContext="loop_while"
          disabled={isReadOnly}
          hideCELHint
          showCELIndicator={false}
        />
      )}

      {isParallel && (
        <p className="text-xs text-muted-foreground">
          Parallel loop outputs are available under <code className="bg-muted px-1 rounded">nodes.{step.id || "loop"}._results</code>, with summary fields for completed, failed, and parallel execution state.
        </p>
      )}
    </>
  );
}