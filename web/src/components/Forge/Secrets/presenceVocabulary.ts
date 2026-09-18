// Copyright (c) 2025 Reliant Labs

/**
 * Presence → pixels.
 *
 * The CONTAINER treatments are imported from ../stateVocabulary, not redefined.
 * Presence and image state are two different vocabularies over the same three
 * certainty categories, and a second set of fills would let them drift apart —
 * at which point "not known" would look like one thing on the topology screen
 * and another here, which defeats the point of having a shared vocabulary at
 * all. Only the ICONS are local, because the reasons differ.
 *
 * The four presences map onto three treatments: the two unknowns (`not-held`,
 * `undetermined`) share the dashed, unfilled container and differ by glyph and
 * sentence. That is the same structure stateVocabulary uses for its three
 * unknown states, and for the same reason — an operator's next action differs
 * between "an external manager owns this" and "nothing was configured", but
 * neither is a pass or a failure.
 */

import { CircleDashed, CircleSlash, KeyRound, Lock, ShieldCheck } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { SecretPresence } from "@/services/forge/secrets";
import { certaintyOfPresence } from "@/services/forge/secrets";

import { CERTAINTY_STYLES } from "../stateVocabulary";
import type { CertaintyStyle } from "../stateVocabulary";

/** styleForPresence resolves through certainty, so presence cannot style itself. */
export function styleForPresence(presence: SecretPresence): CertaintyStyle {
  return CERTAINTY_STYLES[certaintyOfPresence(presence)];
}

/**
 * ICON_BY_PRESENCE. `Lock` for externally-held is the one worth naming: it reads
 * as "someone else is holding this safely", which is the correct impression, and
 * is visibly not the CircleSlash used for a real absence.
 */
export const ICON_BY_PRESENCE: Record<SecretPresence, LucideIcon> = {
  present: ShieldCheck,
  missing: CircleSlash,
  "not-held": Lock,
  undetermined: CircleDashed,
};

export function iconForPresence(presence: SecretPresence): LucideIcon {
  return ICON_BY_PRESENCE[presence] ?? CircleDashed;
}

/** The glyph for an inert store key — a key that fits no lock. */
export const INERT_ICON: LucideIcon = KeyRound;
