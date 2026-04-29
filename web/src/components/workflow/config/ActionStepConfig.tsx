import { useState, useEffect } from "react";
import type {
  Step,
  ActionStep,
  ResponseToolDefinition,
} from "../../../types/workflow";
import { getCatalogClient } from "../../../api/grpc-client";
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
  isReadOnly = false,
}: {
  step: ActionStep;
  onUpdate: (step: Step) => void;
  isReadOnly?: boolean;
}) {
  const [nodes, setNodes] = useState<NodeInfo[]>([]);
  const [loadingNodes, setLoadingNodes] = useState(true);

  useEffect(() => {
    const fetchNodes = async () => {
      try {
        const client = getCatalogClient();
        const response = await client.listNodes({});
        setNodes(response.nodes || []);
      } catch (error) {
        console.error("Failed to fetch nodes:", error);
        setNodes([]);
      } finally {
        setLoadingNodes(false);
      }
    };
    fetchNodes();
  }, []);

  const currentNode = nodes.find((n) => n.id === step.type);
  const allFields = currentNode?.inputFields || [];

  useEffect(() => {
    if (!currentNode || allFields.length === 0) return
    if (!hasTypedArgs(step)) return

    const hasExistingValues = allFields.some(
      (field) => getActionArgValue(step, field.name) !== undefined,
    )
    if (hasExistingValues) return

    const defaults: Array<{ name: string; value: unknown; type: string; isCel: boolean }> = []
    for (const field of allFields) {
      if (field.defaultValue !== undefined && field.defaultValue !== '') {
        defaults.push({
          name: field.name,
          value: field.defaultValue,
          type: field.type,
          isCel: field.isCel,
        })
      }
    }

    if (defaults.length > 0) {
      onUpdate(withActionArgs(step, defaults))
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentNode?.id]);

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

      {loadingNodes ? (
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
