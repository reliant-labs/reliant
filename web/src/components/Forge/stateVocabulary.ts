// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL VOCABULARY. Three levels, not two.
 *
 * This module is the single place where a certainty category becomes pixels, and
 * it exists as its own file so the mapping can be asserted directly by a test
 * rather than inferred from a rendered table.
 *
 * The three levels are distinguished along THREE independent axes at once, so no
 * single one of them has to carry the distinction:
 *
 *   known-good   SOLID fill      continuous border   filled check icon
 *   known-bad    SOLID fill      continuous border   filled alert icon
 *   unknown      NO fill         DASHED border       hollow/outline icon
 *
 * Colour alone would not be enough. A viewer with a colour-vision deficiency,
 * a greyscale screenshot in an incident channel, or a user on a high-contrast
 * theme all need the difference to survive, and "not measured" is precisely the
 * state that must never be mistaken for "measured and fine". The dashed,
 * unfilled treatment reads as provisional at a glance and is the one axis that
 * survives every rendering.
 *
 * known-good and known-bad share a fill treatment and differ by hue and icon —
 * that pair is genuinely a two-state distinction (proven right vs proven wrong)
 * and both are equally CERTAIN. What must not collapse is either of them against
 * `unknown`, which is why the fill/border axis separates the certain pair from
 * the uncertain one rather than separating good from bad.
 *
 * Only semantic tokens are used: success / destructive / muted-foreground /
 * border, all defined in index.css and driven by data-color-scheme and .dark.
 */

import { AlertTriangle, CircleDashed, CircleSlash, CloudOff, CheckCircle2, Tag } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { Certainty } from "@/services/forge/topology";

export interface CertaintyStyle {
  /** Cell/badge container classes. */
  container: string;
  /** Icon + text colour. */
  foreground: string;
}

/**
 * CERTAINTY_STYLES is keyed by category, not by state, so a new forge state
 * cannot arrive with its own bespoke appearance — it inherits its category's
 * treatment, and an unrecognised state falls into `unknown` upstream.
 */
export const CERTAINTY_STYLES: Record<Certainty, CertaintyStyle> = {
  // Solid, filled, continuous border: measured, and it matched.
  "known-good": {
    container: "bg-success/15 border border-solid border-success/40",
    foreground: "text-success",
  },
  // Solid, filled, continuous border: measured, and it did not match.
  "known-bad": {
    container: "bg-destructive/15 border border-solid border-destructive/40",
    foreground: "text-destructive",
  },
  // No fill, DASHED border: nothing was measured. Must not read as either above.
  unknown: {
    container: "bg-transparent border border-dashed border-border",
    foreground: "text-muted-foreground",
  },
};

/**
 * ICON_BY_STATE gives each of the six states its own glyph.
 *
 * The three `unknown` states share a treatment but NOT an icon: "nobody looked
 * yet" (CircleDashed), "we could not reach the cluster" (CloudOff) and "it runs
 * by a mutable tag so the bytes cannot be proven" (Tag) are three different
 * reasons the answer is unknown, and an operator's next action differs for each.
 * Collapsing them to one question mark would hide that.
 */
export const ICON_BY_STATE: Record<string, LucideIcon> = {
  match: CheckCircle2,
  drift: AlertTriangle,
  missing: CircleSlash,
  not_verified: CircleDashed,
  unreachable: CloudOff,
  untagged: Tag,
};

export function iconForState(state: string | undefined): LucideIcon {
  return (state && ICON_BY_STATE[state]) || CircleDashed;
}

/** Human-facing name for a certainty category, used by the legend and header. */
export const CERTAINTY_LABELS: Record<Certainty, string> = {
  "known-good": "Proven",
  "known-bad": "Proven wrong",
  unknown: "Not known",
};

/**
 * CERTAINTY_BLURBS explain why three levels exist, on the screen itself. The
 * `unknown` sentence is the one that stops a reader treating an unverified
 * column as a clean bill of health.
 */
export const CERTAINTY_BLURBS: Record<Certainty, string> = {
  "known-good": "The cluster was read and the running digest is the one this release froze.",
  "known-bad": "The cluster was read and it disagrees with this release.",
  unknown:
    "Nothing was measured, or the bytes cannot be proven. This is neither healthy nor broken — it is unverified.",
};
