// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL VOCABULARY FOR ENV-STATUS CHECKS. Five levels, not two.
 *
 * This extends the three-level scheme stateVocabulary.ts established for the
 * topology screen rather than weakening it. The axes are the same, and they
 * carry the same meanings:
 *
 *   MEASURED   → solid fill, CONTINUOUS border, filled icon
 *   UNMEASURED → no fill,    DASHED border,     hollow/outline icon
 *
 * Topology has three cells to place along those axes; env status has five, so
 * two more distinctions are added WITHIN each half:
 *
 *   measured-good      solid fill   continuous border   filled check       success hue
 *   measured-degraded  solid fill   continuous border   filled triangle    warning hue
 *   measured-bad       solid fill   continuous border   filled octagon     destructive hue
 *   not-applicable     NO fill      DASHED border       hollow minus       muted, DIMMED
 *   undetermined       NO fill      DASHED border       hollow help-circle  muted, RINGED
 *
 * Why the border axis separates measured from unmeasured, rather than separating
 * good from bad: the pair that must never be confused is "measured and fine"
 * against "could not measure". Good and bad are both CERTAIN, and confusing them
 * with each other is not the failure mode anybody has — nobody reads a red row
 * as healthy. Reading a grey row as healthy is exactly what happened for the ten
 * hours three services crashlooped behind an all-green `forge env status`.
 *
 * Why `not-applicable` and `undetermined` still differ, despite sharing the
 * dashed/unfilled half: one is a finished answer and the other is a hole in the
 * report, and forge's doctor package says in its own enum comment that they must
 * never render the same — they used to both be a grey dash, which made "app port
 * 8080 not discovered" indistinguishable from "mkcert not required for this
 * project". They are separated on THREE further axes here: a different icon
 * (minus vs help-circle), opacity (skip is dimmed; it is genuinely less
 * important), and a RING on undetermined, which is the only treatment in the set
 * that draws the eye without claiming a hue. Undetermined is the row you want a
 * reader to stop on.
 *
 * Colour is never the only carrier. A greyscale screenshot pasted into an
 * incident channel, a high-contrast theme, and a viewer with a colour-vision
 * deficiency all have to be able to tell "not measured" from "measured and
 * fine", so fill, border style, icon shape, opacity and ring all survive
 * independently of hue.
 *
 * Only semantic tokens are used: success / warning / destructive /
 * muted-foreground / border, all defined in index.css and driven by
 * data-color-scheme and .dark.
 */

import { AlertTriangle, CheckCircle2, HelpCircle, MinusCircle, OctagonAlert } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { CheckDisposition } from "@/services/forge/status";

export interface DispositionStyle {
  /** Badge/row-chip container classes: fill, border style, and any ring. */
  container: string;
  /** Icon + text colour. */
  foreground: string;
}

/**
 * DISPOSITION_STYLES is keyed by disposition, not by raw status, so a new forge
 * status cannot arrive carrying a bespoke appearance — it inherits its
 * disposition's treatment, and an unrecognised status falls into `undetermined`
 * upstream in services/forge/status.ts.
 */
export const DISPOSITION_STYLES: Record<CheckDisposition, DispositionStyle> = {
  // Measured, correct. Solid, filled, continuous border.
  "measured-good": {
    container: "bg-success/15 border border-solid border-success/40",
    foreground: "text-success",
  },
  // Measured, imperfect. Same certainty treatment, warning hue — NOT destructive:
  // a degraded stack and a broken one are different reports.
  "measured-degraded": {
    container: "bg-warning/15 border border-solid border-warning/40",
    foreground: "text-warning",
  },
  // Measured, wrong. Solid and destructive: the one treatment that means "act".
  "measured-bad": {
    container: "bg-destructive/15 border border-solid border-destructive/40",
    foreground: "text-destructive",
  },
  // A FINISHED answer that happens to be "not here": dashed and unfilled like
  // undetermined, but DIMMED, because it needs no attention at all.
  "not-applicable": {
    container: "bg-transparent border border-dashed border-border opacity-70",
    foreground: "text-muted-foreground",
  },
  // A HOLE in the report. Dashed and unfilled — never mistakable for measured —
  // but ringed and full-opacity so it draws the eye rather than receding like a
  // skip. This is the row whose misrendering as green hid two real outages.
  undetermined: {
    container: "bg-transparent border border-dashed border-muted-foreground/60 ring-1 ring-inset ring-muted-foreground/30",
    foreground: "text-muted-foreground",
  },
};

/**
 * ICON_BY_STATUS gives each status its own glyph, and the two unmeasured ones
 * differ: a minus says "nothing to report here", a question mark says "we do not
 * know". Collapsing them to one glyph would undo the distinction the container
 * treatment works to preserve.
 */
export const ICON_BY_STATUS: Record<string, LucideIcon> = {
  pass: CheckCircle2,
  warn: AlertTriangle,
  fail: OctagonAlert,
  skip: MinusCircle,
  unknown: HelpCircle,
};

/** An unrecognised status gets the undetermined glyph, matching its disposition. */
export function iconForCheckStatus(status: string | undefined): LucideIcon {
  return (status && ICON_BY_STATUS[status]) || HelpCircle;
}

/** Human-facing name for a disposition, used by the legend and the header tally. */
export const DISPOSITION_LABELS: Record<CheckDisposition, string> = {
  "measured-good": "Passed",
  "measured-degraded": "Degraded",
  "measured-bad": "Failed",
  "not-applicable": "Not applicable",
  undetermined: "Could not measure",
};

/**
 * DISPOSITION_BLURBS explain the five levels on the screen itself. The
 * `undetermined` sentence is the one that stops a reader treating a run with
 * holes in it as a clean bill of health, and the `not-applicable` one says
 * explicitly that it is a finished answer, so the two cannot be read as
 * synonyms.
 */
export const DISPOSITION_BLURBS: Record<CheckDisposition, string> = {
  "measured-good": "The check ran, got its facts, and they were correct.",
  "measured-degraded": "The check ran and got its facts. They are not clean, but the measurement happened.",
  "measured-bad": "The check ran, got its facts, and they were wrong.",
  "not-applicable":
    "A finished answer: this project's shape means the check does not apply. Nothing is missing.",
  undetermined:
    "No answer at all — the check could not obtain the facts it needed. This is neither a pass nor a skip; it is a hole in the report.",
};
