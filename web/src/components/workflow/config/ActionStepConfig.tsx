import { useEffect, useRef } from "react";
import type {
  Step,
  ActionStep,
  ResponseToolDefinition,
} from "../../../types/workflow";
import { ResponseToolsEditor } from "../ResponseToolsEditor";
import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { nodeInputFieldToSchema, groupFieldsByCategory } from "../../../lib/nodeFieldAdapter";
import type { NodeInfo } from "../../../gen/reliant/v1/catalog_pb";
import {
  getActionArgValue,
  withActionArg,
  withActionArgs,
  hasTypedArgs,
} from "../../../lib/actionStepArgs";
import { Section, SectionFields, SectionLabel } from "./primitives";

export function ActionStepConfig({
  step,
  onUpdate,
  catalogNodes,
  catalogLoading = false,
  isReadOnly = false,
}: {
  step: ActionStep;
  onUpdate: (step: Step) => void;
  catalogNodes: NodeInfo[];
  catalogLoading?: boolean;
  isReadOnly?: boolean;
}) {
  const currentNode = catalogNodes.find((n) => n.id === step.type);
  const allFields = currentNode?.inputFields || [];

  // Apply field defaults exactly once per (node id, step id) pair. Using a ref
  // instead of useState avoids an extra render and survives parent re-renders
  // that swap the catalog reference without changing its contents.
  const defaultsAppliedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!currentNode || allFields.length === 0) return;
    if (!hasTypedArgs(step)) return;
    const key = `${currentNode.id}::${step.id}`;
    if (defaultsAppliedRef.current === key) return;

    const hasExistingValues = allFields.some(
      (field) => getActionArgValue(step, field.name) !== undefined,
    );
    defaultsAppliedRef.current = key;
    if (hasExistingValues) return;

    const defaults: Array<{ name: string; value: unknown; type: string; isCel: boolean }> = [];
    for (const field of allFields) {
      if (field.defaultValue !== undefined && field.defaultValue !== '') {
        defaults.push({
          name: field.name,
          value: field.defaultValue,
          type: field.type,
          isCel: field.isCel,
        });
      }
    }

    if (defaults.length > 0) {
      onUpdate(withActionArgs(step, defaults));
    }
  // step is read inside but we only want to apply defaults once per (node, step) pair.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentNode?.id, step.id]);

  const handleFieldChange = (fieldName: string, fieldType: string, isCel: boolean, value: unknown) => {
    onUpdate(withActionArg(step, fieldName, value, fieldType, isCel));
  };

  const renderField = (field: (typeof allFields)[number]) => {
    const schema = nodeInputFieldToSchema(field);
    return (
      <ProtoFieldRenderer
        key={field.name}
        schema={schema}
        value={getActionArgValue(step, field.name)}
        onChange={(val) => handleFieldChange(field.name, field.type, field.isCel, val)}
        disabled={isReadOnly}
      />
    );
  };

  const responseTool = getActionArgValue(step, 'response_tool') as ResponseToolDefinition | undefined;

  return (
    <>
      {currentNode?.description && (
        <Section>
          <p className="cpv2-field-hint !mt-0">{currentNode.description}</p>
        </Section>
      )}

      {catalogLoading ? (
        <Section>
          <p className="cpv2-field-hint !mt-0">Loading configuration...</p>
        </Section>
      ) : allFields.length > 0 ? (
        groupFieldsByCategory(allFields).map((group) => (
          <Section key={group.category}>
            <SectionLabel>{group.category || "Configuration"}</SectionLabel>
            <SectionFields>
              {group.fields.map(renderField)}
            </SectionFields>
          </Section>
        ))
      ) : step.type !== 'call_llm' ? (
        <Section>
          <p className="cpv2-field-hint !mt-0 italic">No configuration required</p>
        </Section>
      ) : null}

      {step.type === "call_llm" && (
        <Section>
          <SectionLabel>Response Tool</SectionLabel>
          <ResponseToolsEditor
            tool={responseTool || null}
            onChange={(tool) =>
              onUpdate(withActionArg(step, 'response_tool', tool, 'object', false))
            }
            isReadOnly={isReadOnly}
          />
        </Section>
      )}
    </>
  );
}
