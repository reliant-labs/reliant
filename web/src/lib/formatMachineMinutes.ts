/** Coupon grants are hours on a Small machine; a larger machine uses them
 * faster (a Medium twice as fast). Say so with the size, not with jargon. */

/** Prose form, for sentences: "10 hours on a Small machine". */
export function formatMachineMinutes(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} hour${hours === 1 ? "" : "s"} on a Small machine`;
}

/** Compact form, for stat cards and table cells: "10 h (Small)". */
export function formatMachineMinutesShort(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} h (Small)`;
}
