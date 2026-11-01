/** Exact nonnegative wire quantities. Never use these operations to invent a charge. */
export interface Rational { readonly numerator: bigint; readonly denominator: bigint }
const gcd = (a: bigint, b: bigint): bigint => b === 0n ? a : gcd(b, a % b);
export function rational(numerator: bigint, denominator = 1n): Rational | null {
  if (numerator < 0n || denominator <= 0n) return null;
  const divisor = gcd(numerator, denominator);
  return { numerator: numerator / divisor, denominator: denominator / divisor };
}
/** Strict, bounded input; malformed/missing/negative/nonfinite values are unknown, not zero. */
export function parseRational(value: string | undefined): Rational | null {
  if (!value || value.length > 512) return null;
  const fraction = /^(\d+)\/(\d+)$/.exec(value);
  if (fraction) return rational(BigInt(fraction[1]), BigInt(fraction[2]));
  const decimal = /^(\d+)(?:\.(\d+))?$/.exec(value);
  if (!decimal) return null;
  const tail = decimal[2] ?? "";
  return rational(BigInt(decimal[1] + tail), 10n ** BigInt(tail.length));
}
export const addRational = (a: Rational, b: Rational): Rational => rational(a.numerator * b.denominator + b.numerator * a.denominator, a.denominator * b.denominator)!;
export const multiplyRational = (a: Rational, b: Rational): Rational => rational(a.numerator * b.numerator, a.denominator * b.denominator)!;
export const divideRational = (a: Rational, b: Rational): Rational | null => rational(a.numerator * b.denominator, a.denominator * b.numerator);
export const compareRational = (a: Rational, b: Rational): number => a.numerator * b.denominator < b.numerator * a.denominator ? -1 : a.numerator * b.denominator > b.numerator * a.denominator ? 1 : 0;
export function formatRational(value: Rational, places = 1, rounding: "nearest" | "floor" = "nearest"): string {
  const scale = 10n ** BigInt(places);
  const scaled = value.numerator * scale;
  const units = scaled / value.denominator + (rounding === "nearest" && scaled % value.denominator * 2n >= value.denominator ? 1n : 0n);
  const digits = units.toString().padStart(places + 1, "0");
  return places ? `${digits.slice(0, -places)}.${digits.slice(-places)}` : digits;
}
export function exactHours(value: string | undefined): string | null {
  const seconds = parseRational(value);
  return seconds ? formatRational(divideRational(seconds, rational(3600n)!)!) : null;
}
export function exactCentsLabel(value: string | undefined): string | null {
  const cents = parseRational(value);
  return cents ? `$${formatRational(divideRational(cents, rational(100n)!)!, 2)}` : null;
}
/** Bounded chart geometry ONLY, never compare balances or calculate dollar charges with it. */
export function displayRatio(a: Rational, b: Rational): number | null {
  const ratio = divideRational(a, b);
  if (!ratio) return null;
  const fixed = ratio.numerator * 1_000_000n / ratio.denominator;
  return Number(fixed > 1_000_000n ? 1_000_000n : fixed) / 1_000_000;
}
