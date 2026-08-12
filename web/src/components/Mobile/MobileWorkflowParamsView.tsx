/**
 * Read-only display of a workflow's declared inputs, plus preset selection.
 *
 * Deliberately not `WorkflowParamsPanel` — that panel renders every input as
 * an *editable* field via `ProtoFieldRenderer`, which for CEL-typed inputs
 * mounts `CELInput` → `MonacoCELEditor`. Editing workflow params is
 * authoring-adjacent (`chatWorkflowParams` is desktop-only in the capability
 * map), so this reads the same `Input` definitions with the plain getters in
 * `lib/inputHelpers.ts` and shows them as text — no editable field, no CEL,
 * no Monaco.
 */

import type { Workflow } from "../../types/workflow";
import {
  getInputDefault,
  getInputDescription,
  type InputDef,
} from "../../lib/inputHelpers";
import { formatValueForDisplay, hasDefaultValue } from "../../lib/paramUtils";
import { PresetPicker } from "../workflow/PresetPicker";
import type { Preset } from "../../store/globalDataStore";

interface ParamRowProps {
  name: string;
  input: InputDef;
}

function ParamRow({ name, input }: ParamRowProps) {
  const description = getInputDescription(input);
  const defaultValue = getInputDefault(input);
  // hasDefaultValue reads a flat `{ type, default }` shape — the proto
  // Input's default lives inside the config oneof, not at the top level, so
  // it has to be extracted via getInputDefault() first.
  const hasDefault = hasDefaultValue({ type: input.type, default: defaultValue });

  return (
    <div className="border-b border-border px-4 py-3">
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium text-foreground">{name}</span>
        <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {input.type}
        </span>
      </div>
      {description && (
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      )}
      {hasDefault && (
        <p className="mt-1 text-xs text-muted-foreground">
          Default: <span className="text-foreground">{formatValueForDisplay(defaultValue)}</span>
        </p>
      )}
    </div>
  );
}

interface MobileWorkflowParamsViewProps {
  workflow: Workflow;
  projectId?: string;
  presets: Preset[];
  selectedPreset?: string | null;
  onSelectPreset?: (preset: Preset | null) => void;
}

export function MobileWorkflowParamsView({
  workflow,
  projectId,
  presets,
  selectedPreset,
  onSelectPreset,
}: MobileWorkflowParamsViewProps) {
  const inputEntries = Object.entries(workflow.inputs ?? {});

  return (
    <div className="flex h-full min-h-0 flex-col overflow-y-auto">
      {presets.length > 0 && (
        <div className="border-b border-border px-4 py-3">
          <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            Preset
          </div>
          <PresetPicker
            presets={presets}
            value={selectedPreset}
            onChange={onSelectPreset}
            projectId={projectId}
            workflowName={workflow.name}
          />
        </div>
      )}

      {inputEntries.length === 0 ? (
        <div className="px-4 py-8 text-center text-sm text-muted-foreground">
          This workflow takes no parameters.
        </div>
      ) : (
        inputEntries.map(([name, input]) => (
          <ParamRow key={name} name={name} input={input} />
        ))
      )}
    </div>
  );
}
