// Copyright (c) 2025 Reliant Labs

/**
 * THE PROMOTE VISUAL VOCABULARY.
 *
 * Two independent classifications become pixels here, and this is a separate
 * file so both mappings can be asserted directly by a test rather than inferred
 * from a rendered diff — the same reason stateVocabulary.ts exists next door.
 * The certainty vocabulary is IMPORTED where it applies; it is not forked.
 *
 * ── DIRECTION ──────────────────────────────────────────────────────────────
 *
 * A rollback must not be a subtitle on a forward promote. Three axes separate
 * them at once, so no single one carries the distinction:
 *
 *   ahead    solid border   ArrowUp     neutral/foreground treatment
 *   behind   SOLID border   RotateCcw   DESTRUCTIVE treatment + the word ROLLBACK
 *   same     DASHED border  Equal       muted — nothing would move
 *   initial  DASHED border  Sparkles    muted — there is no "from"
 *   unknown  DASHED border  HelpCircle  muted — the ordering could not place it
 *
 * Colour alone would not be enough: a greyscale screenshot in an incident
 * channel, a high-contrast theme, and a reviewer with a colour-vision
 * deficiency all need the difference to survive. So `behind` additionally
 * carries a distinct ICON and the literal word "Rollback" in its label, and the
 * banner renders its own heading rather than a tinted version of the forward
 * one. The three non-moving directions share a dashed, unfilled treatment
 * because none of them is an action with a direction.
 *
 * ── IMAGE CHANGE ───────────────────────────────────────────────────────────
 *
 * `added` and `removed` are STRUCTURAL: they alter which images the binding
 * declares, rather than moving a digest underneath an image that stays. They
 * are separated from `changed` on the axis a skimming reader cannot miss — a
 * leading +/− sigil and a solid left rule, versus an arrow between two digests.
 * They are deliberately NOT a third colour of the same chip.
 *
 * Only semantic tokens: destructive / success / warning / muted-foreground /
 * border / foreground, all defined in index.css and driven by data-color-scheme
 * and .dark.
 */

import {
  ArrowDownCircle,
  ArrowUp,
  Equal,
  HelpCircle,
  Minus,
  Plus,
  RotateCcw,
  Sparkles,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { ChangeShape, PromoteDirection, PromoteImageChange } from "@/services/forge/promote";

// ── Direction ───────────────────────────────────────────────────────────────

export interface DirectionStyle {
  /** Banner container classes. */
  container: string;
  /** Icon + emphasis colour. */
  foreground: string;
  /** Short label. For `behind` this contains the word "Rollback". */
  label: string;
  /** One sentence, used when forge sends no direction_detail. */
  blurb: string;
  icon: LucideIcon;
}

export const DIRECTION_STYLES: Record<PromoteDirection, DirectionStyle> = {
  // Moving forward. Solid border: this is a real move with a direction.
  ahead: {
    container: "border border-solid border-border bg-muted/30",
    foreground: "text-foreground",
    label: "Forward promote",
    blurb: "The target release was cut after the one this environment runs now.",
    icon: ArrowUp,
  },
  // A ROLLBACK. Destructive treatment, its own icon, and the word in the label.
  behind: {
    container: "border border-solid border-destructive/50 bg-destructive/10",
    foreground: "text-destructive",
    label: "Rollback — moves backwards",
    blurb:
      "The target release was cut BEFORE the one this environment runs now. This moves the environment backwards.",
    icon: RotateCcw,
  },
  // Nothing would move. Dashed: not an action with a direction.
  same: {
    container: "border border-dashed border-border bg-transparent",
    foreground: "text-muted-foreground",
    label: "Already on this release",
    blurb: "This environment is already bound to the target release.",
    icon: Equal,
  },
  initial: {
    container: "border border-dashed border-border bg-transparent",
    foreground: "text-muted-foreground",
    label: "First promote",
    blurb: "This environment has never been promoted, so there is no release to move from.",
    icon: Sparkles,
  },
  unknown: {
    container: "border border-dashed border-border bg-transparent",
    foreground: "text-muted-foreground",
    label: "Direction unknown",
    blurb:
      "The release ordering could not place both releases, so whether this moves forward or backwards is not known.",
    icon: HelpCircle,
  },
};

/** True for the one direction that needs a reviewer to stop and read. */
export function isDestructiveDirection(direction: PromoteDirection): boolean {
  return direction === "behind";
}

// ── Image change ────────────────────────────────────────────────────────────

export interface ChangeStyle {
  /** Row classes. Structural changes get a solid left rule. */
  row: string;
  /** Label + sigil colour. */
  foreground: string;
  /** A leading glyph: + / − for structural, none for a version move. */
  sigil: string;
  label: string;
  icon: LucideIcon;
  /**
   * What this does to the binding, in one line. Scoped carefully: promote
   * writes a pointer, so `removed` stops the binding DECLARING an image and
   * deletes nothing from any cluster.
   */
  blurb: string;
}

export const CHANGE_STYLES: Record<PromoteImageChange, ChangeStyle> = {
  // STRUCTURAL: the binding gains an image it did not declare.
  added: {
    row: "border-l-2 border-solid border-success bg-success/10",
    foreground: "text-success",
    sigil: "+",
    label: "Added",
    icon: Plus,
    blurb: "The binding gains this image. It was not declared before.",
  },
  // STRUCTURAL: the binding stops declaring an image. Note the careful scope —
  // nothing is deleted from a cluster by a promote.
  removed: {
    row: "border-l-2 border-solid border-destructive bg-destructive/10",
    foreground: "text-destructive",
    sigil: "−",
    label: "Removed",
    icon: Minus,
    blurb:
      "The binding stops declaring this image. Nothing is deleted from the cluster by promoting — the next deploy simply no longer pins it.",
  },
  // A version move under an image that stays. No sigil, no left rule.
  changed: {
    row: "border-l-2 border-transparent",
    foreground: "text-warning",
    sigil: "",
    label: "Changed",
    icon: ArrowDownCircle,
    blurb: "The same image, pinned to a different digest.",
  },
  unchanged: {
    row: "border-l-2 border-transparent",
    foreground: "text-muted-foreground",
    sigil: "",
    label: "Unchanged",
    icon: Equal,
    blurb: "The environment already runs this digest for this image.",
  },
  unknown: {
    row: "border-l-2 border-dashed border-border",
    foreground: "text-muted-foreground",
    sigil: "?",
    label: "Unclassified",
    icon: HelpCircle,
    blurb:
      "This build of reliant does not recognise the classification forge reported, so what happens to this image is not known.",
  },
};

/** Human-facing name for the coarse shape, used by the legend. */
export const SHAPE_LABELS: Record<ChangeShape, string> = {
  structural: "Structural",
  version: "Version",
  none: "No change",
  unknown: "Unclassified",
};
