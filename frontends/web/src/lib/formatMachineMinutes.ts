/** Coupon grants are hours on an M machine (the coupon unit); a larger machine
 * uses them faster (an L twice as fast). Say so with the size, not with jargon. */

/** Prose form, for sentences: "10 hours on an M machine". */
export function formatMachineMinutes(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} hour${hours === 1 ? "" : "s"} on an M machine`;
}

/** Compact form, for stat cards and table cells: "10 h (M)". */
export function formatMachineMinutesShort(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} h (M)`;
}

/** A grant balance, which the server reports in coupon-unit (M) seconds, as
 * prose: "10 hours on an M machine". Whole minutes only. */
export function formatMachineSeconds(seconds: number): string {
  return formatMachineMinutes(Math.floor(seconds / 60));
}

/** formatMachineSeconds' compact form: "10 h (M)". */
export function formatMachineSecondsShort(seconds: number): string {
  return formatMachineMinutesShort(Math.floor(seconds / 60));
}
