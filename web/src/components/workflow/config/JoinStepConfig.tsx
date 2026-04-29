import { useEffect } from "react";
import type { JoinStep } from "../../../types/workflow";
import { directCel } from "../../../lib/celAdapter";
import { getConditionExpression, getJoinCondition } from "./shared";
import { FieldLabel, FieldSelect, Section, SectionLabel } from "./primitives";

export function JoinStepConfig({
  step,
  onUpdate,
  isReadOnly = false,
}: {
  step: JoinStep;
  onUpdate: (updates: Partial<JoinStep>) => void;
  isReadOnly?: boolean;
}) {
  const condition = getJoinCondition(step.condition);

  useEffect(() => {
    if (!getConditionExpression(step.condition)) {
      onUpdate({ condition: directCel("all") });
    }
  }, [step.condition, onUpdate]);

  return (
    <Section>
      <SectionLabel>Join</SectionLabel>
      <FieldLabel>Join Condition</FieldLabel>
      <FieldSelect
        value={condition}
        onChange={(e) => onUpdate({ condition: directCel(e.target.value) })}
        disabled={isReadOnly}
      >
        <option value="all">all - Wait for all incoming branches</option>
        <option value="any">any - Continue when first branch completes</option>
      </FieldSelect>
      <p className="cpv2-field-hint">
        {condition === "all"
          ? "Workflow continues after all incoming edges have triggered."
          : "Workflow continues when any incoming edge triggers."}
      </p>
    </Section>
  );
}
