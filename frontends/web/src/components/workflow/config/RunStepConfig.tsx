import type { RunStep } from "../../../types/workflow";
import { getStepCommand, withRunArgs } from "../../../types/workflow";
import { celString } from "../../../lib/celAdapter";
import { getStringValue, TimeoutSelector } from "./shared";
import { FieldLabel, FieldTextarea, Section, SectionFields, SectionLabel } from "./primitives";

export function RunStepConfig({
  step,
  onUpdate,
  isReadOnly = false,
}: {
  step: RunStep;
  onUpdate: (step: RunStep) => void;
  isReadOnly?: boolean;
}) {
  return (
    <>
      <Section>
        <SectionLabel>Command</SectionLabel>
        <FieldLabel>Command</FieldLabel>
        <FieldTextarea
          value={getStepCommand(step)}
          onChange={(e) =>
            onUpdate(
              withRunArgs(step, { command: celString(e.target.value) }) as RunStep,
            )
          }
          rows={3}
          placeholder="make lint"
          disabled={isReadOnly}
        />
      </Section>

      <Section>
        <SectionLabel>Execution</SectionLabel>
        <SectionFields>
          <TimeoutSelector
            value={getStringValue(step.timeout) || undefined}
            onChange={(val) =>
              onUpdate({ ...step, timeout: val ? celString(val) : undefined })
            }
            disabled={isReadOnly}
          />
        </SectionFields>
      </Section>
    </>
  );
}
