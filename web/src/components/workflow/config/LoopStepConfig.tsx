import { Edit3, Eye } from "lucide-react";
import type { LoopStep } from "../../../types/workflow";
import {
  getStepInline,
  getStepWhile,
  getStepInputs,
  withLoopArgs,
} from "../../../types/workflow";
import { CELExpressionInput } from "../CELInput";
import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { inputDefToSchema } from "../../../lib/nodeFieldAdapter";
import { getInputUI } from "../../../lib/inputHelpers";
import { directCel } from "../../../lib/celAdapter";

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
    </>
  );
}