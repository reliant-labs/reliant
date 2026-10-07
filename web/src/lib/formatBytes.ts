// Copyright (c) 2025 Reliant Labs

const UNITS = ["B", "KB", "MB", "GB", "TB"] as const;

/** "1.4 GB". Decimal units, one decimal place under 10, none above. */
export function formatBytes(bytes: number | bigint): string {
  let value = Math.max(0, Number(bytes));
  let unit = 0;
  while (value >= 1000 && unit < UNITS.length - 1) {
    value /= 1000;
    unit += 1;
  }
  const text = unit === 0 || value >= 10 ? Math.round(value).toString() : value.toFixed(1);
  return `${text} ${UNITS[unit]}`;
}
