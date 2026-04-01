import { useEffect } from "react";
import type { JoinStep } from "../../../types/workflow";
import { directCel } from "../../../lib/celAdapter";
import { getConditionExpression, getJoinCondition } from "./shared";

export function JoinStepConfig({
  step,
  onUpdate,
  isReadOnly = false,
}: {
  step: JoinStep;
  onUpdate: (updates: Partial<JoinStep>) => void;
  isReadOnly?: boolean;
}) {
  // Default to "all" if condition is not set (e.g., imported workflow without condition)
  const condition = getJoinCondition(step.condition);

  // Persist the default if it was missing
  useEffect(() => {
    if (!getConditionExpression(step.condition)) {
      onUpdate({ condition: directCel("all") });
    }
  }, [step.condition, onUpdate]);

  return (
    <>
      <div>
        <label className="block text-xs font-medium text-muted-foreground mb-1">
          Join Condition
        </label>
        <select
          value={condition}
          onChange={(e) => onUpdate({ condition: directCel(e.target.value) })}
          className="w-full px-3 py-2 border border-input rounded-md focus:ring-2 focus:ring-ring focus:border-ring bg-background text-foreground disabled:opacity-60 disabled:cursor-not-allowed"
          disabled={isReadOnly}
        >
          <option value="all">all - Wait for all incoming branches</option>
          <option value="any">
            any - Continue when first branch completes
          </option>
        </select>
        <p className="mt-1 text-xs text-muted-foreground">
          {condition === "all"
            ? "Workflow continues after ALL incoming edges have triggered"
            : "Workflow continues when ANY incoming edge triggers"}
        </p>
      </div>
    </>
  );
}
