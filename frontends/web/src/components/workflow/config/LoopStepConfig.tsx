import { useEffect, useRef } from "react";
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

const ITEMS_SCHEMA: ProtoFieldSchema = {
  key: "items",
  label: "Items",
  widget: "text",
  valueKind: "string",
  celExpressionOnly: true,
  placeholder: "nodes.decompose.output.components",
  helpText:
    "CEL expression that evaluates to a list or map of iteration items for parallel execution.",
};

const KEY_SCHEMA: ProtoFieldSchema = {
  key: "key",
  label: "Key (optional)",
  widget: "text",
  valueKind: "string",
  celExpressionOnly: true,
  placeholder: "iter.item.name",
  helpText:
    "CEL expression used to key each iteration in nodes.<loop>._results. Defaults to the iteration index.",
};

const WHILE_SCHEMA: ProtoFieldSchema = {
  key: "while",
  label: "Continue while",
  widget: "text",
  valueKind: "string",
  celExpressionOnly: true,
  placeholder: "outputs.done == true",
  helpText:
    "CEL expression that ends the loop when true. Access iteration outputs via 'outputs' namespace.",
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

  // Initialize inline workflow lazily — but only ONCE per step id. Without
  // this guard, every time the user selects a loop step that genuinely has no
  // inline body, the effect fires and marks the workflow dirty with no real
  // user edit. The ref ensures we only auto-initialize the first time we see
  // a given step lacking an inline body.
  const initializedStepRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (inlineWorkflow) return;
    if (isReadOnly) return;
    if (initializedStepRef.current === step.id) return;
    initializedStepRef.current = step.id;
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
    // We intentionally only run when the inline body is missing; including
    // `step` or `onUpdate` would re-run on every keystroke and overwrite edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inlineWorkflow, step.id, isReadOnly]);

  return (
    <>
      <Section>
        <FieldMode value={modeValue} onChange={handleModeChange} disabled={isReadOnly} />
      </Section>

      {isParallel ? (
        <Section>
          <SectionLabel>Iteration</SectionLabel>
          <SectionFields>
            <ProtoFieldRenderer
              schema={ITEMS_SCHEMA}
              value={itemsValue}
              onChange={(value) =>
                onUpdate(
                  withLoopArgs(step, {
                    items: typeof value === "string" && value ? celString(value) : undefined,
                  }) as LoopStep,
                )
              }
              disabled={isReadOnly}
              celContext="default"
            />

            <ProtoFieldRenderer
              schema={KEY_SCHEMA}
              value={keyValue}
              onChange={(value) =>
                onUpdate(
                  withLoopArgs(step, {
                    key: typeof value === "string" && value ? value : undefined,
                  }) as LoopStep,
                )
              }
              disabled={isReadOnly}
              celContext="default"
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
          <ProtoFieldRenderer
            schema={WHILE_SCHEMA}
            value={whileValue}
            onChange={(value) =>
              onUpdate(
                withLoopArgs(step, {
                  while: typeof value === "string" && value ? directCel(value) : undefined,
                }) as LoopStep,
              )
            }
            disabled={isReadOnly}
            celContext="loop_while"
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