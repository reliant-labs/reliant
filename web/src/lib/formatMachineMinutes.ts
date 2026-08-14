/**
 * Machine-time formatting.
 *
 * Minutes are the unit the backend meters (heartbeat buckets → minutes → the
 * Stripe meter), but hours are how people think about machine time, so both
 * forms below show both numbers. They live together so the two phrasings
 * cannot drift apart.
 */

/** Prose form, for sentences: "600 machine minutes (10 hours)". */
export function formatMachineMinutes(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${minutes} machine minute${minutes === 1 ? "" : "s"} (${hoursLabel} hour${
    hours === 1 ? "" : "s"
  })`;
}

/** Compact form, for stat cards and table cells: "600 min (10 h)". */
export function formatMachineMinutesShort(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${minutes} min (${hoursLabel} h)`;
}
