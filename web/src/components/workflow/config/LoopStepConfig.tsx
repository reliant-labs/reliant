import { Edit3, Eye } from "lucide-react";
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
import { DrillRow, ModeGroup, ModePill, Section, SectionFields, SectionLabel } from "./primitives";

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
    <Section>
      <SectionLabel>Loop Inputs</SectionLabel>
      <SectionFields>
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
      </SectionFields>
    </Section>
  );
}

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
  const whileValue = getStepWhile(step);
  const keyValue = getStepKey(step);
  const onFailureValue = getStepOnFailure(step) || "continue";
  const isParallel = parallelValue === true || (typeof parallelValue === "string" && parallelValue.length > 0);
  const modeValue = isParallel ? "parallel" : "sequential";

  const handleModeChange = (nextMode: "parallel" | "sequential") => {
    if (nextMode === modeValue) return;

    if (nextMode === "parallel") {
      onUpdate(
        withLoopArgs(step, {
          parallel: { value: { case: "literal" as const, value: true } } as any,
          while: undefined,
        }) as LoopStep,
      );
      return;
    }

    onUpdate(
      withLoopArgs(step, {
        parallel: undefined,
        items: undefined,
        key: undefined,
        onFailure: undefined,
      }) as LoopStep,
    );
  };

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
      <Section>
        <FieldMode value={modeValue} onChange={handleModeChange} disabled={isReadOnly} />
      </Section>

      {isParallel ? (
        <Section>
          <SectionLabel>Iteration</SectionLabel>
          <SectionFields>
            <CELExpressionInput
              label="Items"
              helpTooltip="CEL expression that evaluates to a list or map of iteration items for parallel execution."
              value={itemsValue}
              onChange={(val) =>
                onUpdate(
                  withLoopArgs(step, {
                    items: val ? celString(val) as any : undefined,
                  }) as LoopStep,
                )
              }
              placeholder="nodes.decompose.output.components"
              celContext="default"
              disabled={isReadOnly}
              hideCELHint
              showCELIndicator={false}
            />

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
              placeholder="iter.item.name"
              celContext="default"
              disabled={isReadOnly}
              hideCELHint
              showCELIndicator={false}
            />

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
          </SectionFields>
        </Section>
      ) : (
        <Section>
          <SectionLabel>While Condition</SectionLabel>
          <CELExpressionInput
            label="Continue while"
            helpTooltip="CEL expression that ends the loop when true. Access iteration outputs via 'outputs' namespace."
            value={whileValue}
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
        </Section>
      )}

      <Section>
        <SectionLabel>Loop Body</SectionLabel>
        <DrillRow
          label={isReadOnly ? "View sub-workflow" : "Edit sub-workflow"}
          sublabel={`${inlineWorkflow?.nodes?.length || 0} nodes, ${inlineWorkflow?.edges?.length || 0} edges`}
          icon={isReadOnly ? <Eye className="w-3 h-3" /> : <Edit3 className="w-3 h-3" />}
          onClick={() => onEditLoopBody?.(step)}
        />
      </Section>

      {inlineWorkflow?.inputs && Object.keys(inlineWorkflow.inputs).length > 0 && (
        <InlineLoopInputsEditor
          inputs={inlineWorkflow.inputs}
          values={stepInputs}
          onChange={(inputs) =>
            onUpdate(withLoopArgs(step, { args: inputs as any }) as LoopStep)
          }
        />
      )}
    </>
  );
}

function FieldMode({
  value,
  onChange,
  disabled,
}: {
  value: "parallel" | "sequential";
  onChange: (value: "parallel" | "sequential") => void;
  disabled: boolean;
}) {
  return (
    <div className="cpv2-field-inline">
      <span className="cpv2-fi-label">Mode</span>
      <ModeGroup>
        <ModePill active={value === "sequential"} onClick={() => !disabled && onChange("sequential")}>Sequential</ModePill>
        <ModePill active={value === "parallel"} onClick={() => !disabled && onChange("parallel")}>Parallel</ModePill>
      </ModeGroup>
    </div>
  );
}