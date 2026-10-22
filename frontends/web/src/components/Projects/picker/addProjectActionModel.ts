import type { LucideIcon } from "lucide-react";

/**
 * One way to add a project, as the picker renders it — in the page header
 * once projects exist, and as a start tile when there are none. Both
 * renderings read this one shape so they can never disagree about what an
 * action is called, whether it is available, or why not.
 */
export interface AddProjectAction {
  kind: "clone" | "open" | "new";
  label: string;
  /** What it does — or, when disabled, why it can't be used right now. */
  description: string;
  icon: LucideIcon;
  onClick: () => void;
  disabled: boolean;
  /** The primary action: accent-filled, and first among the start tiles. */
  lead: boolean;
  testId: string;
}
