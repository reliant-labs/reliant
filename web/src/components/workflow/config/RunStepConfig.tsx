import type { RunStep } from "../../../types/workflow";
import { getStepCommand, withRunArgs } from "../../../types/workflow";
import { celString } from "../../../lib/celAdapter";
import { getStringValue, TimeoutSelector } from "./shared";

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
      <div>
        <label className="block text-xs font-medium text-foreground mb-1">
          Command
        </label>
        <textarea
          value={getStepCommand(step)}
          onChange={(e) =>
            onUpdate(
              withRunArgs(step, { command: celString(e.target.value) as any }) as RunStep,
            )
          }
          className="w-full px-3 py-2 border border-input rounded-md bg-background text-foreground font-mono text-sm focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
          rows={3}
          placeholder="make lint"
          disabled={isReadOnly}
        />
      </div>

      <TimeoutSelector
        value={getStringValue(step.timeout) || undefined}
        onChange={(val) =>
          onUpdate({ ...step, timeout: val ? celString(val) : undefined })
        }
        disabled={isReadOnly}
      />
    </>
  );
}