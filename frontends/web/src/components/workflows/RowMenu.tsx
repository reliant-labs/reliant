// Copyright (c) 2025 Reliant Labs

/**
 * A row's overflow menu: every action that is not the row's primary one,
 * reachable from the keyboard (nothing in the Workflows area is hover-only;
 * WORKFLOW_UI.md §2.2). Used by Library rows and preset rows.
 */

import Tooltip from "../forge-ui/tooltip";
import { MoreHorizontal } from "lucide-react";

import DropdownMenu from "../forge-ui/dropdown_menu";

export interface RowMenuAction {
  label: string;
  onSelect: () => void;
  destructive?: boolean;
}

export function RowMenu({ label, actions }: { label: string; actions: RowMenuAction[] }) {
  if (actions.length === 0) return null;
  return (
    <DropdownMenu
      align="right"
      label={label}
      menuClassName="min-w-44"
      trigger={(props) => (
        <Tooltip content={label} side="left" delay={300} triggerClassName="inline-flex">
          <button
            type="button"
            {...props}
            aria-label={label}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
          </button>
        </Tooltip>
      )}
      groups={[
        {
          items: actions.map((action) => ({
            label: action.label,
            variant: action.destructive ? "danger" : "default",
            onClick: action.onSelect,
          })),
        },
      ]}
    />
  );
}
