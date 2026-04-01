// Copyright (c) 2025 Reliant Labs
// Demo component for ObjectSchemaEditor - can be accessed via /demo route

import { useState } from "react";
import { ObjectSchemaEditor } from "./ObjectSchemaEditor";
import type { InputSchema } from "./WorkflowParamInput";

export function ObjectSchemaEditorDemo() {
  // Example schema for a code review result
  const [schema] = useState<InputSchema>({
    type: "object",
    description: "Code review result structure",
    properties: {
      verdict: {
        type: "boolean",
        description: "true = pass, false = fail",
      },
      confidence: {
        type: "integer",
        description: "Confidence level from 1-10",
        minimum: 1,
        maximum: 10,
      },
      findings: {
        type: "string",
        description: "Detailed review findings",
        minLength: 10,
      },
      issues: {
        type: "array",
        description: "List of identified issues",
      },
      severity: {
        type: "string",
        description: "Issue severity level",
        enum: ["low", "medium", "high", "critical"],
      },
      metadata: {
        type: "object",
        description: "Additional metadata",
        properties: {
          reviewer: {
            type: "string",
            description: "Name of the reviewer",
          },
          timestamp: {
            type: "string",
            description: "Review timestamp",
          },
        },
        required: ["reviewer"],
      },
    },
    required: ["verdict", "findings"],
  });

  const [value, setValue] = useState<unknown>({
    verdict: true,
    confidence: 8,
    findings: "Code looks good overall with minor suggestions",
    issues: [],
    severity: "low",
    metadata: {
      reviewer: "AI Agent",
      timestamp: new Date().toISOString(),
    },
  });

  return (
    <div className="min-h-screen bg-background p-8">
      <div className="max-w-4xl mx-auto space-y-8">
        <div>
          <h1 className="text-3xl font-bold text-foreground mb-2">
            ObjectSchemaEditor Demo
          </h1>
          <p className="text-muted-foreground">
            Visual JSON Schema editor for structured workflow inputs
          </p>
        </div>

        <div className="grid grid-cols-1 lg:grid-cols-2 gap-8">
          {/* Editor */}
          <div className="space-y-4">
            <div>
              <h2 className="text-xl font-semibold mb-2">Editor</h2>
              <p className="text-sm text-muted-foreground mb-4">
                Edit the values based on the defined schema
              </p>
            </div>

            <ObjectSchemaEditor
              schema={schema}
              value={value}
              onChange={setValue}
            />
          </div>

          {/* JSON Output */}
          <div className="space-y-4">
            <div>
              <h2 className="text-xl font-semibold mb-2">JSON Output</h2>
              <p className="text-sm text-muted-foreground mb-4">
                Current value as JSON
              </p>
            </div>

            <pre className="p-4 bg-muted rounded-lg text-sm font-mono overflow-auto max-h-[600px] border border-border">
              {JSON.stringify(value, null, 2)}
            </pre>
          </div>
        </div>

        {/* Schema Display */}
        <div className="space-y-4">
          <div>
            <h2 className="text-xl font-semibold mb-2">Schema Definition</h2>
            <p className="text-sm text-muted-foreground mb-4">
              The JSON Schema that defines the object structure
            </p>
          </div>

          <pre className="p-4 bg-muted rounded-lg text-sm font-mono overflow-auto max-h-96 border border-border">
            {JSON.stringify(schema, null, 2)}
          </pre>
        </div>

        {/* Feature Showcase */}
        <div className="space-y-4">
          <h2 className="text-xl font-semibold mb-2">Features</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="p-4 border border-border rounded-lg bg-card">
              <h3 className="font-semibold mb-2">Type-Specific Inputs</h3>
              <p className="text-sm text-muted-foreground">
                String, number, boolean, array, and nested object support
              </p>
            </div>
            <div className="p-4 border border-border rounded-lg bg-card">
              <h3 className="font-semibold mb-2">Constraints</h3>
              <p className="text-sm text-muted-foreground">
                Min/max values, length constraints, enum validation
              </p>
            </div>
            <div className="p-4 border border-border rounded-lg bg-card">
              <h3 className="font-semibold mb-2">Required Fields</h3>
              <p className="text-sm text-muted-foreground">
                Visual indicators for required properties
              </p>
            </div>
            <div className="p-4 border border-border rounded-lg bg-card">
              <h3 className="font-semibold mb-2">Nested Objects</h3>
              <p className="text-sm text-muted-foreground">
                Recursive rendering with collapsible sections
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
