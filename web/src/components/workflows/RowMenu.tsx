// Copyright (c) 2025 Reliant Labs

/**
 * A row's overflow menu: every action that is not the row's primary one,
 * reachable from the keyboard (nothing in the Workflows area is hover-only;
 * WORKFLOW_UI.md §2.2). Used by Library rows and preset rows.
 */

import { useState } from "react";
import { MoreHorizontal } from "lucide-react";

import { cn } from "@/lib/utils";
import { Dropdown } from "../ui/Dropdown";

export interface RowMenuAction {
  label: string;
  onSelect: () => void;
  destructive?: boolean;
}

export function RowMenu({ label, actions }: { label: string; actions: RowMenuAction[] }) {
  const [open, setOpen] = useState(false);
  if (actions.length === 0) return null;
  return (
    <Dropdown
      isOpen={open}
      onOpenChange={setOpen}
      align="right"
      variant="form"
      contentClassName="min-w-44 py-1"
      trigger={
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-label={label}
          aria-haspopup="menu"
          aria-expanded={open}
          className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </button>
      }
    >
      <div role="menu" aria-label={label}>
        {actions.map((action) => (
          <button
            key={action.label}
            type="button"
            role="menuitem"
            onClick={() => {
              setOpen(false);
              action.onSelect();
            }}
            className={cn(
              "flex w-full items-center px-3 py-1.5 text-left text-sm transition-colors hover:bg-muted/60 focus:bg-muted/60 focus:outline-none",
              action.destructive ? "text-destructive" : "text-foreground",
            )}
          >
            {action.label}
          </button>
        ))}
      </div>
    </Dropdown>
  );
}
