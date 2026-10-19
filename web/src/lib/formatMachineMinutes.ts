/** Coupon grants arrive in minutes of small-machine time. Render weighted
 * hours, never imply that a larger machine uses the grant at the same rate. */

/** Prose form, for sentences: "600 machine minutes (10 hours)". */
export function formatMachineMinutes(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} small-machine-hour${hours === 1 ? "" : "s"}`;
}

/** Compact form, for stat cards and table cells: "600 min (10 h)". */
export function formatMachineMinutesShort(minutes: number): string {
  const hours = minutes / 60;
  const hoursLabel = Number.isInteger(hours) ? hours.toString() : hours.toFixed(1);
  return `${hoursLabel} small-machine-hours`;
}
