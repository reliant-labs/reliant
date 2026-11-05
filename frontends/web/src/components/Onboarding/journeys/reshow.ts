/**
 * Which journey to re-offer after a version bump. Pure, and apart from the
 * store so the wizard can ask without the store module's internals.
 */

import { JOURNEY_IDS, type JourneyId } from "../tourParam";
import type { JourneyRecord } from "../../../store/tourStore";

/**
 * The journey to re-offer automatically, or null. Only a journey whose
 * current version has no outcome yet, which the user completed an older
 * version of, and that `reshowAllowed` permits (content opt-in, available
 * here). The user's last-chosen journey wins a tie.
 */
export function journeyToReshow(
  state: { journeys?: Partial<Record<JourneyId, JourneyRecord>>; lastJourney?: JourneyId | null },
  reshowAllowed: (journey: JourneyId) => boolean,
): JourneyId | null {
  const last = state.lastJourney ?? null;
  const order = last ? [last, ...JOURNEY_IDS.filter((id) => id !== last)] : [...JOURNEY_IDS];
  for (const id of order) {
    const record = state.journeys?.[id];
    if (record && record.current === null && record.completedOlder && reshowAllowed(id)) return id;
  }
  return null;
}
