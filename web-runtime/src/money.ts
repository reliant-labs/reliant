// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Money and basis points as INTEGERS, formatted and parsed at the edge of the
// UI. This is the web counterpart of forge/pkg/money: an amount is an integer
// count of its currency's minor unit (cents for USD, yen for JPY, fils for
// KWD) plus an ISO 4217 code, and no float ever sits between the wire and the
// screen. protobuf-es surfaces an int64 as a `bigint`; keep it one until it is
// rendered, and turn user input straight back into one.
//
//   formatMinorUnits(123456n)                            -> "$1,234.56"
//   formatMinorUnits(1234n, { currency: "JPY" })         -> "¥1,234"
//   formatMinorUnits(123456n, { currency: "EUR", locale: "de-DE" }) -> "1.234,56 €"
//   parseMinorUnits("$1,234.5")                          -> 123450n
//   parseMinorUnits("1.234")                             -> null (USD has 2 digits)
//   minorUnitsToInput(123450n)                           -> "1234.50"
//   currencyMinorDigits("KWD")                           -> 3
//   formatBasisPoints(825)                               -> "8.25%"
//   parseBasisPoints("8.25%")                            -> 825
//   basisPointsToInput(850)                              -> "8.5"
//
// THE RULES
//
//   - Amounts are `bigint | number`. A `number` must be an integer: anything
//     else throws a RangeError, because 12.5 "cents" is a major-unit value
//     that leaked into a minor-unit field and rounding it would hide the bug.
//     `null` / `undefined` format as "—" and write an input value of "".
//   - `currency` defaults to "USD", and "" means the default too (a proto3
//     string field that was never set arrives as ""). A malformed code throws
//     Intl's RangeError. `locale` defaults to "en-US".
//   - A currency's minor-unit digits come from Intl (CLDR) —
//     `resolvedOptions().maximumFractionDigits` — and formatting and parsing
//     both use that one source, so they cannot disagree. Note CLDR, not ISO
//     4217, is the authority: HUF and a few others differ.
//   - Formatting is exact at any size. The amount is handed to Intl as a
//     decimal STRING (Intl.NumberFormat v3 — Node 20+, every evergreen browser
//     since 2023), never as a Number, so int64 extremes print to the last
//     digit. An engine without v3 coerces the string to a Number, which is
//     still exact below about 10^15 minor units.
//   - Parsing returns null rather than guessing. Accepted: ASCII digits or
//     the locale's own (Arabic-Indic for ar-EG), surrounding and grouping
//     whitespace, the locale's group and decimal separators, the currency's
//     own symbol or ISO code before or after the number, a leading "+", and —
//     only with `allowNegative` — a leading "-" or "−". Anything Intl
//     rendered for the same options therefore parses back. Refused:
//     more fraction digits than the currency has (never rounded), another
//     currency's symbol, a misplaced group separator ("12,50" typed into an
//     en-US field is a decimal comma, so it is not read as 1,250.00), and an
//     amount outside the int64 range the wire carries.
//   - minorUnitsToInput writes the locale's decimal separator with no
//     grouping, so it round-trips through parseMinorUnits given the same
//     options. For an `<input type="number">` keep the en-US default: that
//     element's value is always "."-decimal whatever the page's locale.
//   - Basis points are an integer count of 0.01%. Parsing allows at most two
//     decimals of a percent and caps at 100% (10000) unless `max` says
//     otherwise — the typical rate (tax, discount, commission) lives in
//     0–100%, and the cap catches "825" typed where "8.25" was meant.
//
// Differences from forge/pkg/money: the Go Format is locale-neutral with its
// own symbol table, while this renders through Intl for the caller's locale;
// and the Go Parse accepts a negative amount, while this one makes negatives
// opt-in, because most amounts a form collects cannot be below zero.
//
// This module is in the BARREL: it imports nothing, so there is no optional
// dependency for a subpath to fence off.

/** Currency and locale for a money helper. Both optional. */
export interface MoneyOptions {
  /** ISO 4217 code. Default "USD"; "" (a proto3 unset string) also means USD. */
  currency?: string;
  /** BCP 47 locale for separators and symbol placement. Default "en-US". */
  locale?: string;
}

/** Options for {@link parseMinorUnits}. */
export interface ParseMoneyOptions extends MoneyOptions {
  /** Accept a leading "-" / "−". Default false: a minus sign returns null. */
  allowNegative?: boolean;
}

/** Locale for a basis-point helper. */
export interface BasisPointsOptions {
  /** BCP 47 locale for separators and the percent sign. Default "en-US". */
  locale?: string;
}

/** Options for {@link parseBasisPoints}. */
export interface ParseBasisPointsOptions extends BasisPointsOptions {
  /** Accept a leading "-" / "−". Default false: a minus sign returns null. */
  allowNegative?: boolean;
  /** Largest magnitude accepted, in basis points. Default 10000 (100%). */
  max?: number;
}

const DEFAULT_CURRENCY = "USD";
const DEFAULT_LOCALE = "en-US";
const UNSET = "—";
const BASIS_POINT_DIGITS = 2; // 1 bp = 0.01 percent
const DEFAULT_MAX_BASIS_POINTS = 10_000;
const INT64_MAX = 9_223_372_036_854_775_807n;
const INT64_MIN = -9_223_372_036_854_775_808n;

/**
 * formatMinorUnits renders an integer minor-unit amount as currency for a
 * locale: 123456n → "$1,234.56", 1234n JPY → "¥1,234". Exact at any size.
 * Unset renders "—"; a non-integer number throws RangeError.
 */
export function formatMinorUnits(
  minor: bigint | number | null | undefined,
  options: MoneyOptions = {},
): string {
  if (minor === null || minor === undefined) return UNSET;
  const currency = currencyOf(options);
  const decimal = toDecimalString(
    toInteger(minor),
    currencyMinorDigits(currency),
  );
  return currencyFormat(localeOf(options), currency).format(decimal);
}

/**
 * parseMinorUnits reads user input such as "1,234.5", "$1234.56" or "12" into
 * minor units. Returns null for anything it cannot represent exactly — too
 * many decimals, junk, a misplaced separator, a negative amount without
 * `allowNegative`, or a value beyond int64 — so a form rejects it instead of
 * silently rounding.
 */
export function parseMinorUnits(
  input: string,
  options: ParseMoneyOptions = {},
): bigint | null {
  const currency = currencyOf(options);
  const value = parseScaled(
    input,
    currencyMinorDigits(currency),
    moneyGrammar(localeOf(options), currency),
    options.allowNegative ?? false,
  );
  if (value === null || value > INT64_MAX || value < INT64_MIN) return null;
  return value;
}

/**
 * minorUnitsToInput renders minor units as the value of an editable field:
 * 123456n → "1234.56" (de-DE: "1234,56"). No grouping, every minor digit
 * shown, and the result parses back through parseMinorUnits. Unset → "";
 * a non-integer number throws RangeError.
 */
export function minorUnitsToInput(
  minor: bigint | number | null | undefined,
  options: MoneyOptions = {},
): string {
  if (minor === null || minor === undefined) return "";
  const currency = currencyOf(options);
  const decimal = toDecimalString(
    toInteger(minor),
    currencyMinorDigits(currency),
  );
  return decimal.replace(
    ".",
    moneyGrammar(localeOf(options), currency).decimal,
  );
}

/**
 * currencyMinorDigits returns how many decimal digits a currency's minor unit
 * has — USD 2, JPY 0, KWD 3 — as Intl reports it. Useful for an input's
 * `step` or a validation message. Throws RangeError on a malformed code.
 */
export function currencyMinorDigits(currency?: string): number {
  const code = currency || DEFAULT_CURRENCY;
  // CLDR's digits are per currency, not per locale, so en-US answers for all.
  return cached(
    minorDigitsCache,
    code,
    () =>
      currencyFormat(DEFAULT_LOCALE, code).resolvedOptions()
        .maximumFractionDigits ?? 2,
  );
}

/**
 * formatBasisPoints renders an integer basis-point rate as a percentage:
 * 825 → "8.25%", 1000 → "10%". Unset renders "—"; a non-integer number
 * (a percent passed where basis points belong) throws RangeError.
 */
export function formatBasisPoints(
  bps: bigint | number | null | undefined,
  options: BasisPointsOptions = {},
): string {
  if (bps === null || bps === undefined) return UNSET;
  // Intl's percent style multiplies by 100, so hand it the fraction: 825 bp
  // is "0.0825".
  const fraction = toDecimalString(toInteger(bps), BASIS_POINT_DIGITS + 2);
  return percentFormat(localeOf(options)).format(fraction);
}

/**
 * parseBasisPoints reads a percentage such as "8.25" or "8.25%" into basis
 * points (825). Returns null for more than two decimals, junk, a negative
 * rate without `allowNegative`, or a magnitude above `max` (default 100%).
 */
export function parseBasisPoints(
  input: string,
  options: ParseBasisPointsOptions = {},
): number | null {
  const value = parseScaled(
    input,
    BASIS_POINT_DIGITS,
    percentGrammar(localeOf(options)),
    options.allowNegative ?? false,
  );
  if (value === null) return null;
  const bps = Number(value);
  const max = options.max ?? DEFAULT_MAX_BASIS_POINTS;
  if (!Number.isSafeInteger(bps) || Math.abs(bps) > max) return null;
  return bps;
}

/**
 * basisPointsToInput renders basis points as the value of an editable percent
 * field: 825 → "8.25", 850 → "8.5", 1000 → "10". Parses back through
 * parseBasisPoints. Unset → "".
 */
export function basisPointsToInput(
  bps: bigint | number | null | undefined,
  options: BasisPointsOptions = {},
): string {
  if (bps === null || bps === undefined) return "";
  const decimal = toDecimalString(toInteger(bps), BASIS_POINT_DIGITS).replace(
    /\.?0+$/,
    "",
  );
  return decimal.replace(".", percentGrammar(localeOf(options)).decimal);
}

// --- internals ---------------------------------------------------------------

/** A decimal string Intl.NumberFormat v3 formats exactly. */
type DecimalString = `${number}`;

/** How a locale spells a number, as far as parsing it back needs to know. */
interface NumberGrammar {
  decimal: string;
  /** Group separator; whitespace-like separators are normalised to " ". */
  group: string;
  /** Symbols and codes that may precede or follow the number, longest first. */
  affixes: string[];
  /** A well-formed grouped integer part: "1,234,567" or "12,34,567". */
  groupedInteger: RegExp;
  /** The locale's own digits mapped to ASCII; empty when it uses ASCII. */
  nativeDigits: Map<string, string>;
}

const minorDigitsCache = new Map<string, number>();
const formatCache = new Map<string, Intl.NumberFormat>();
const grammarCache = new Map<string, NumberGrammar>();

function currencyOf(options: MoneyOptions): string {
  return options.currency || DEFAULT_CURRENCY;
}

function localeOf(options: { locale?: string }): string {
  return options.locale || DEFAULT_LOCALE;
}

function toInteger(value: bigint | number): bigint {
  if (typeof value === "bigint") return value;
  if (!Number.isInteger(value)) {
    throw new RangeError(
      `forge-web-runtime money: ${value} is not an integer — amounts and rates are integer minor units / basis points`,
    );
  }
  return BigInt(value);
}

/** toDecimalString(123456n, 2) → "1234.56"; (-5n, 2) → "-0.05"; (7n, 0) → "7". */
function toDecimalString(value: bigint, digits: number): DecimalString {
  const negative = value < 0n;
  const magnitude = (negative ? -value : value)
    .toString()
    .padStart(digits + 1, "0");
  const whole = digits === 0 ? magnitude : magnitude.slice(0, -digits);
  const fraction = digits === 0 ? "" : `.${magnitude.slice(-digits)}`;
  return `${negative ? "-" : ""}${whole}${fraction}` as DecimalString;
}

function cached<T>(cache: Map<string, T>, key: string, build: () => T): T {
  let value = cache.get(key);
  if (value === undefined) {
    value = build();
    cache.set(key, value);
  }
  return value;
}

function currencyFormat(locale: string, currency: string): Intl.NumberFormat {
  return cached(
    formatCache,
    `currency|${locale}|${currency}`,
    () => new Intl.NumberFormat(locale, { style: "currency", currency }),
  );
}

function percentFormat(locale: string): Intl.NumberFormat {
  return cached(
    formatCache,
    `percent|${locale}`,
    () =>
      new Intl.NumberFormat(locale, {
        style: "percent",
        maximumFractionDigits: BASIS_POINT_DIGITS,
      }),
  );
}

function moneyGrammar(locale: string, currency: string): NumberGrammar {
  return cached(grammarCache, `currency|${locale}|${currency}`, () => {
    // Read the separators off a formatter forced to show a fraction and a
    // group, so a zero-digit currency (JPY) still reveals its decimal mark.
    const separators = new Intl.NumberFormat(locale, {
      style: "currency",
      currency,
      minimumFractionDigits: 1,
      maximumFractionDigits: 1,
    }).formatToParts(1234567.5);
    // The currency's own symbols: as this locale writes them, and as en-US
    // does ("¥" beside ja-JP's full-width "￥"), each in the default and the
    // narrow form ("CA$" and "$"). Never another currency's.
    const symbols = [locale, DEFAULT_LOCALE].flatMap((symbolLocale) =>
      (["symbol", "narrowSymbol"] as const).map(
        (currencyDisplay) =>
          new Intl.NumberFormat(symbolLocale, {
            style: "currency",
            currency,
            currencyDisplay,
          })
            .formatToParts(1)
            .find((part) => part.type === "currency")?.value ?? "",
      ),
    );
    return buildGrammar(locale, separators, [...symbols, currency]);
  });
}

function percentGrammar(locale: string): NumberGrammar {
  return cached(grammarCache, `percent|${locale}`, () => {
    const parts = new Intl.NumberFormat(locale, {
      style: "percent",
      minimumFractionDigits: 1,
      maximumFractionDigits: 1,
    }).formatToParts(12345.675);
    const percentSign =
      parts.find((part) => part.type === "percentSign")?.value ?? "%";
    return buildGrammar(locale, parts, [percentSign, "%"]);
  });
}

function buildGrammar(
  locale: string,
  parts: Intl.NumberFormatPart[],
  affixes: string[],
): NumberGrammar {
  // Locales such as ar-EG render Arabic-Indic digits; read them back as
  // ASCII so a formatted amount parses. ASCII digits are always accepted too.
  const digitFormat = new Intl.NumberFormat(locale, { useGrouping: false });
  const nativeDigits = new Map<string, string>();
  for (let digit = 0; digit <= 9; digit++) {
    const native = digitFormat.format(digit);
    if (native !== String(digit)) nativeDigits.set(native, String(digit));
  }
  const decimal = parts.find((part) => part.type === "decimal")?.value ?? ".";
  const rawGroup = parts.find((part) => part.type === "group")?.value ?? ",";
  const group = /^\s$/.test(rawGroup) ? " " : rawGroup;
  const g = escapeRegExp(group);
  return {
    decimal,
    group,
    affixes: [...new Set(affixes.map(stripInvisible).filter(Boolean))].sort(
      (a, b) => b.length - a.length,
    ),
    // First group 1–3 digits, inner groups 2–3 (Western and Indian
    // grouping), last group exactly 3. The last-group rule is the one that
    // refuses a decimal comma typed where the locale groups with commas.
    groupedInteger: new RegExp(`^\\d{1,3}(?:${g}\\d{2,3})*${g}\\d{3}$`),
    nativeDigits,
  };
}

/**
 * parseScaled reads a decimal number in the given grammar and returns it
 * scaled by 10^digits as an exact bigint, or null when the input is not a
 * number in that grammar or carries more than `digits` fraction digits.
 */
function parseScaled(
  input: string,
  digits: number,
  grammar: NumberGrammar,
  allowNegative: boolean,
): bigint | null {
  let text = stripInvisible(input).trim();
  if (grammar.nativeDigits.size > 0) {
    text = Array.from(
      text,
      (char) => grammar.nativeDigits.get(char) ?? char,
    ).join("");
  }
  let sign = "";
  const takeSign = (): void => {
    if (sign === "" && /^[+\-\u2212]/.test(text)) {
      sign = text.charAt(0);
      text = text.slice(1).trimStart();
    }
  };
  takeSign(); // "-$12"
  text = stripAffix(text, grammar.affixes);
  takeSign(); // "$-12"

  // Any run of whitespace left inside the number is grouping, held to the
  // same placement rule as the locale's own separator: "1 234" passes,
  // "12 50" does not.
  text = text.replace(/\s+/g, grammar.group);

  const [integerPart = "", fractionPart, extra] = text.split(grammar.decimal);
  if (extra !== undefined) return null;
  if (integerPart === "" && !fractionPart) return null;
  if (
    integerPart !== "" &&
    !/^\d+$/.test(integerPart) &&
    !grammar.groupedInteger.test(integerPart)
  ) {
    return null;
  }
  if (
    fractionPart !== undefined &&
    (!/^\d*$/.test(fractionPart) || fractionPart.length > digits)
  ) {
    return null;
  }

  const negative = sign === "-" || sign === "\u2212";
  if (negative && !allowNegative) return null;

  const whole = BigInt(integerPart.split(grammar.group).join("") || "0");
  const fraction = BigInt((fractionPart ?? "").padEnd(digits, "0") || "0");
  const magnitude = whole * 10n ** BigInt(digits) + fraction;
  return negative ? -magnitude : magnitude;
}

/** Remove one currency symbol / code / percent sign from either end. */
function stripAffix(text: string, affixes: string[]): string {
  for (const affix of affixes) {
    // Case-insensitive so "usd 12" reads like "USD 12". Compared slice by
    // slice: upper-casing the whole input could change its length.
    if (text.slice(0, affix.length).toUpperCase() === affix.toUpperCase()) {
      return text.slice(affix.length).trimStart();
    }
    if (text.slice(-affix.length).toUpperCase() === affix.toUpperCase()) {
      return text.slice(0, -affix.length).trimEnd();
    }
  }
  return text;
}

/**
 * Drop the bidi controls Intl places around numbers in right-to-left locales
 * (LRM, RLM, ALM, embeddings, isolates). Invisible, so a pasted amount
 * carries them without the user knowing.
 */
function stripInvisible(text: string): string {
  return text.replace(/[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g, "");
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
