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

  // Fetch nodes from gRPC on mount
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

  // Apply field defaults for new nodes with no args set
  useEffect(() => {
    if (!currentNode || allFields.length === 0) return
    if (!hasTypedArgs(step)) return

    // Only apply if no args fields have values yet (new node)
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

  // Read response_tool from proto args (CallLLMArgs.responseTool)
  const responseTool = getActionArgValue(step, 'response_tool') as ResponseToolDefinition | undefined;

  return (
    <>
      {/* Activity description */}
      {currentNode?.description && (
        <p className="text-xs text-muted-foreground mb-4">
          {currentNode.description}
        </p>
      )}

      {/* Dynamic input fields based on node metadata */}
      {loadingNodes ? (
        <div className="text-sm text-muted-foreground">
          Loading configuration...
        </div>
      ) : allFields.length > 0 ? (
        <div className="space-y-4">
          {groupFieldsByCategory(allFields).map((group, idx) => (
            <div key={group.category}>
              {idx > 0 && (
                <div className="h-px bg-border/30 my-3" />
              )}
              <div className="space-y-3">
                {group.fields.map(renderField)}
              </div>
            </div>
          ))}
        </div>
      ) : step.type !== 'call_llm' ? (
        <p className="text-sm text-muted-foreground italic">No configuration required</p>
      ) : null}

      {/* Response Tool editor - only for call_llm action */}
      {step.type === "call_llm" && (
        <div className="mt-4 pt-4 border-t border-border">
          <ResponseToolsEditor
            tool={responseTool || null}
            onChange={(tool) =>
              onUpdate(withActionArg(step, 'response_tool', tool, 'object', false))
            }
            isReadOnly={isReadOnly}
          />
        </div>
      )}
    </>
  );
}
