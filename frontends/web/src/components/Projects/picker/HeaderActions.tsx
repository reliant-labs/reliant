import { useId } from "react";

import { Tooltip } from "@/components/ui/Tooltip";

import type { AddProjectAction } from "./addProjectActionModel";
import { pickerButton } from "./buttonStyles";

/**
 * The page header's add-project buttons.
 *
 * Forge's convention puts the primary action rightmost, so the leading action
 * renders last with the accent fill and the rest sit to its left as
 * secondaries. Every button carries its description twice: as the Tooltip
 * (sighted, on hover/focus) and as an aria-describedby (screen readers, and
 * the only channel for a DISABLED button, which takes no focus). A disabled
 * clone must say why, because hiding the reason is what used to strand users
 * whose only machine had failed.
 */
export function HeaderActions({ actions }: { actions: AddProjectAction[] }) {
  const ordered = [...actions.filter((a) => !a.lead), ...actions.filter((a) => a.lead)];
  return (
    <div className="flex flex-wrap items-center gap-2">
      {ordered.map((action) => (
        <HeaderActionButton key={action.kind} action={action} />
      ))}
    </div>
  );
}

function HeaderActionButton({ action }: { action: AddProjectAction }) {
  const descriptionId = useId();
  const Icon = action.icon;
  return (
    <Tooltip content={action.description} placement="bottom" delay={300}>
      <button
        type="button"
        onClick={action.onClick}
        disabled={action.disabled}
        aria-describedby={descriptionId}
        data-testid={action.testId}
        data-variant={action.lead ? "primary" : "secondary"}
        className={pickerButton(action.lead ? "primary" : "secondary")}
      >
        <Icon className="h-4 w-4" aria-hidden="true" />
        {action.label}
        <span id={descriptionId} className="sr-only">
          {action.description}
        </span>
      </button>
    </Tooltip>
  );
}
