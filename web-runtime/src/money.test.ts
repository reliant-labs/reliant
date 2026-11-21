// The money contract. The first block is lifted case for case from a forge app
// (roofers) that had to hand-write these helpers before the runtime carried
// them; the rest is what generalising them to any currency and locale has to
// hold: Intl's minor-unit digits (JPY 0, KWD 3), exactness past 2^53, and
// parsing that refuses to guess rather than rounding or misreading a comma.
import { describe, expect, it } from "vitest";

import {
  basisPointsToInput,
  currencyMinorDigits,
  formatBasisPoints,
  formatMinorUnits,
  minorUnitsToInput,
  parseBasisPoints,
  parseMinorUnits,
} from "./money.js";

const INT64_MAX = 9_223_372_036_854_775_807n;
const INT64_MIN = -9_223_372_036_854_775_808n;
const NBSP = "\u00a0";

describe("the cases roofers hand-wrote", () => {
  it("formats cents as US dollars with grouping", () => {
    expect(formatMinorUnits(123456n)).toBe("$1,234.56");
    expect(formatMinorUnits(5n)).toBe("$0.05");
    expect(formatMinorUnits(0n)).toBe("$0.00");
    expect(formatMinorUnits(-2550n)).toBe("-$25.50");
    expect(formatMinorUnits(1999)).toBe("$19.99");
  });

  it("renders unset as an em dash", () => {
    expect(formatMinorUnits(undefined)).toBe("—");
    expect(formatMinorUnits(null)).toBe("—");
  });

  it("accepts common dollar spellings", () => {
    expect(parseMinorUnits("1,234.56")).toBe(123456n);
    expect(parseMinorUnits("$12")).toBe(1200n);
    expect(parseMinorUnits("12.5")).toBe(1250n);
    expect(parseMinorUnits(" 0.07 ")).toBe(7n);
  });

  it("rejects input it cannot represent exactly", () => {
    expect(parseMinorUnits("")).toBeNull();
    expect(parseMinorUnits("abc")).toBeNull();
    expect(parseMinorUnits("1.234")).toBeNull();
    expect(parseMinorUnits("-5")).toBeNull();
  });

  it("round-trips through minorUnitsToInput", () => {
    expect(minorUnitsToInput(123456n)).toBe("1234.56");
    expect(minorUnitsToInput(1200n)).toBe("12.00");
    expect(parseMinorUnits(minorUnitsToInput(987654n))).toBe(987654n);
  });

  it("formats and parses basis points as percentages", () => {
    expect(formatBasisPoints(825)).toBe("8.25%");
    expect(formatBasisPoints(1000)).toBe("10%");
    expect(formatBasisPoints(0)).toBe("0%");
    expect(parseBasisPoints("8.25")).toBe(825);
    expect(parseBasisPoints("30%")).toBe(3000);
    expect(parseBasisPoints("101")).toBeNull();
  });

  it("renders basis points for an editable percent field", () => {
    // roofers' edit page carried its own bpsToPercentInput for exactly this.
    expect(basisPointsToInput(825)).toBe("8.25");
    expect(basisPointsToInput(850)).toBe("8.5");
    expect(basisPointsToInput(1000)).toBe("10");
  });
});

describe("currencyMinorDigits", () => {
  it("reads the currency's minor-unit digits from Intl", () => {
    expect(currencyMinorDigits("USD")).toBe(2);
    expect(currencyMinorDigits("EUR")).toBe(2);
    expect(currencyMinorDigits("JPY")).toBe(0);
    expect(currencyMinorDigits("KWD")).toBe(3);
  });

  it("defaults to USD, including for a proto3 unset (empty) currency", () => {
    expect(currencyMinorDigits()).toBe(2);
    expect(currencyMinorDigits("")).toBe(2);
    expect(formatMinorUnits(150n, { currency: "" })).toBe("$1.50");
  });

  it("throws on a malformed currency code rather than guessing one", () => {
    expect(() => currencyMinorDigits("US")).toThrow(RangeError);
    expect(() => formatMinorUnits(1n, { currency: "dollars" })).toThrow(
      RangeError,
    );
  });
});

describe("a currency with no minor unit (JPY)", () => {
  it("formats the count as whole yen", () => {
    expect(formatMinorUnits(1234n, { currency: "JPY" })).toBe("¥1,234");
    expect(formatMinorUnits(0n, { currency: "JPY" })).toBe("¥0");
  });

  it("parses whole yen and refuses any fraction", () => {
    expect(parseMinorUnits("1,234", { currency: "JPY" })).toBe(1234n);
    expect(parseMinorUnits("¥500", { currency: "JPY" })).toBe(500n);
    expect(parseMinorUnits("12.5", { currency: "JPY" })).toBeNull();
  });

  it("accepts the full-width yen sign Japanese locales render", () => {
    const ja = { currency: "JPY", locale: "ja-JP" };
    expect(parseMinorUnits("￥1,234", ja)).toBe(1234n);
    expect(parseMinorUnits("¥1,234", ja)).toBe(1234n);
  });

  it("writes an input value with no decimal point", () => {
    expect(minorUnitsToInput(1234n, { currency: "JPY" })).toBe("1234");
  });
});

describe("a currency with three minor digits (KWD)", () => {
  const kwd = { currency: "KWD" };

  it("formats three fraction digits", () => {
    expect(formatMinorUnits(1234n, kwd)).toBe(`KWD${NBSP}1.234`);
    expect(formatMinorUnits(5n, kwd)).toBe(`KWD${NBSP}0.005`);
  });

  it("parses up to three fraction digits and no more", () => {
    expect(parseMinorUnits("1.234", kwd)).toBe(1234n);
    expect(parseMinorUnits("1.2", kwd)).toBe(1200n);
    expect(parseMinorUnits("KWD 1.234", kwd)).toBe(1234n);
    expect(parseMinorUnits("1.2345", kwd)).toBeNull();
  });

  it("writes an input value with three fraction digits", () => {
    expect(minorUnitsToInput(1234n, kwd)).toBe("1.234");
    expect(minorUnitsToInput(5n, kwd)).toBe("0.005");
  });
});

describe("amounts beyond 2^53", () => {
  it("formats exactly, with no float in the path", () => {
    // 2^53 + 1 is the first integer a double cannot hold; through Number this
    // would print ...409.92.
    expect(formatMinorUnits(9_007_199_254_740_993n)).toBe(
      "$90,071,992,547,409.93",
    );
    expect(formatMinorUnits(INT64_MAX)).toBe("$92,233,720,368,547,758.07");
    expect(formatMinorUnits(INT64_MIN)).toBe("-$92,233,720,368,547,758.08");
    expect(formatMinorUnits(INT64_MAX, { currency: "JPY" })).toBe(
      "¥9,223,372,036,854,775,807",
    );
  });

  it("writes and parses input values exactly", () => {
    expect(minorUnitsToInput(INT64_MAX)).toBe("92233720368547758.07");
    expect(parseMinorUnits("92,233,720,368,547,758.07")).toBe(INT64_MAX);
    expect(
      parseMinorUnits(minorUnitsToInput(INT64_MIN), { allowNegative: true }),
    ).toBe(INT64_MIN);
  });

  it("refuses an amount the int64 wire type cannot carry", () => {
    expect(parseMinorUnits("92233720368547758.08")).toBeNull();
    expect(
      parseMinorUnits("-92233720368547758.09", { allowNegative: true }),
    ).toBeNull();
  });
});

describe("parseMinorUnits", () => {
  it("rejects a minus sign unless negatives are allowed", () => {
    expect(parseMinorUnits("-12.50")).toBeNull();
    expect(parseMinorUnits("-0")).toBeNull();
    const signed = { allowNegative: true };
    expect(parseMinorUnits("-12.50", signed)).toBe(-1250n);
    expect(parseMinorUnits("-$12.50", signed)).toBe(-1250n);
    expect(parseMinorUnits("$-12.50", signed)).toBe(-1250n);
    // U+2212, the minus sign Intl renders for sv-SE and others.
    expect(parseMinorUnits("\u221212.50", signed)).toBe(-1250n);
  });

  it("accepts a leading plus, a bare fraction and a trailing point", () => {
    expect(parseMinorUnits("+12")).toBe(1200n);
    expect(parseMinorUnits(".5")).toBe(50n);
    expect(parseMinorUnits("12.")).toBe(1200n);
  });

  it("accepts the currency's own symbol or ISO code on either side", () => {
    expect(parseMinorUnits("12 USD")).toBe(1200n);
    expect(parseMinorUnits("usd 12")).toBe(1200n);
    expect(parseMinorUnits("CA$12", { currency: "CAD" })).toBe(1200n);
    expect(parseMinorUnits("$12", { currency: "CAD" })).toBe(1200n);
  });

  it("refuses another currency's symbol instead of silently relabelling it", () => {
    expect(parseMinorUnits("€12")).toBeNull();
    expect(parseMinorUnits("$$12")).toBeNull();
  });

  it("refuses a malformed group separator rather than misreading it", () => {
    // "12,50" is a decimal comma typed into an en-US field. Stripping the
    // comma would store 1250.00 — reject it so the form can ask again.
    expect(parseMinorUnits("12,50")).toBeNull();
    expect(parseMinorUnits("12 50")).toBeNull();
    expect(parseMinorUnits("1,2,3")).toBeNull();
    expect(parseMinorUnits("1,,234")).toBeNull();
    expect(parseMinorUnits(",123")).toBeNull();
    expect(parseMinorUnits("1,234,")).toBeNull();
    expect(parseMinorUnits("1.2.3")).toBeNull();
    expect(parseMinorUnits(".")).toBeNull();
    // Well-formed grouping, Western and Indian, is fine.
    expect(parseMinorUnits("1,234,567.89")).toBe(123456789n);
    expect(parseMinorUnits("12,34,567.89")).toBe(123456789n);
    expect(parseMinorUnits("1 234.56")).toBe(123456n);
  });
});

describe("locales", () => {
  const de = { currency: "EUR", locale: "de-DE" };

  it("formats with the locale's separators and symbol placement", () => {
    expect(formatMinorUnits(123456n, de)).toBe(`1.234,56${NBSP}€`);
    // The formatter cache is keyed by locale AND currency.
    expect(formatMinorUnits(123456n)).toBe("$1,234.56");
  });

  it("parses with the locale's separators", () => {
    expect(parseMinorUnits(`1.234,56${NBSP}€`, de)).toBe(123456n);
    expect(parseMinorUnits("1234,56", de)).toBe(123456n);
    expect(parseMinorUnits("€ 1.234", de)).toBe(123400n);
    // A dot-decimal typed into a German field: "." is the group separator
    // there and "50" is not a group, so it is refused, not read as 1250.
    expect(parseMinorUnits("12.50", de)).toBeNull();
  });

  it("reads the locale's native digits as well as ASCII ones", () => {
    const ar = { currency: "EGP", locale: "ar-EG" };
    // ١٢٣٤٫٥٦ — Arabic-Indic digits with the Arabic decimal separator.
    expect(
      parseMinorUnits("\u0661\u0662\u0663\u0664\u066b\u0665\u0666", ar),
    ).toBe(123456n);
    expect(parseMinorUnits("1234\u066b56", ar)).toBe(123456n);
  });

  it("writes input values in the locale's decimal separator", () => {
    expect(minorUnitsToInput(123456n, de)).toBe("1234,56");
    expect(parseMinorUnits(minorUnitsToInput(123456n, de), de)).toBe(123456n);
  });

  it("parses back whatever formatMinorUnits rendered", () => {
    // fr-FR groups with U+202F and sv-SE signs with U+2212; de-CH groups with
    // an apostrophe; es-ES does not group four-digit amounts. The exact
    // strings vary by ICU version, so the contract asserted is the round
    // trip, not the spelling.
    const cases: Array<{ currency: string; locale: string }> = [
      { currency: "USD", locale: "en-US" },
      { currency: "EUR", locale: "de-DE" },
      { currency: "EUR", locale: "fr-FR" },
      { currency: "EUR", locale: "es-ES" },
      { currency: "SEK", locale: "sv-SE" },
      { currency: "CHF", locale: "de-CH" },
      { currency: "INR", locale: "en-IN" },
      { currency: "JPY", locale: "ja-JP" },
      { currency: "KWD", locale: "ar-KW-u-nu-latn" },
      // Native digits: Arabic-Indic, and Devanagari by explicit request.
      { currency: "KWD", locale: "ar-KW" },
      { currency: "INR", locale: "hi-IN-u-nu-deva" },
      { currency: "GBP", locale: "en-GB" },
    ];
    for (const options of cases) {
      for (const amount of [0n, 7n, 1234n, 123456789n, -98765n, INT64_MAX]) {
        const rendered = formatMinorUnits(amount, options);
        expect(
          parseMinorUnits(rendered, { ...options, allowNegative: true }),
          `${options.locale}/${options.currency}: ${JSON.stringify(rendered)}`,
        ).toBe(amount);
      }
    }
  });
});

describe("integer discipline", () => {
  it("throws on a fractional number instead of rounding it", () => {
    // 12.5 "cents" is a major-unit amount that leaked into a minor-unit
    // field; formatting it as $0.13 would hide the bug.
    expect(() => formatMinorUnits(12.5)).toThrow(RangeError);
    expect(() => minorUnitsToInput(0.1)).toThrow(RangeError);
    expect(() => formatMinorUnits(Number.NaN)).toThrow(RangeError);
    expect(() => formatBasisPoints(8.25)).toThrow(RangeError);
  });

  it("accepts an integer number the same as a bigint", () => {
    expect(formatMinorUnits(-5)).toBe("-$0.05");
    expect(minorUnitsToInput(1234)).toBe("12.34");
  });

  it("renders an unset value as empty for an input", () => {
    expect(minorUnitsToInput(null)).toBe("");
    expect(minorUnitsToInput(undefined)).toBe("");
    expect(basisPointsToInput(null)).toBe("");
  });
});

describe("basis points", () => {
  it("formats negative, bigint and fractional-percent rates", () => {
    expect(formatBasisPoints(-50)).toBe("-0.5%");
    expect(formatBasisPoints(1n)).toBe("0.01%");
    expect(formatBasisPoints(10000)).toBe("100%");
    expect(formatBasisPoints(null)).toBe("—");
  });

  it("refuses more precision than a basis point", () => {
    expect(parseBasisPoints("8.255")).toBeNull();
    expect(parseBasisPoints("0.01")).toBe(1);
    expect(parseBasisPoints(".5")).toBe(50);
  });

  it("caps at 100% by default, and the cap is adjustable", () => {
    expect(parseBasisPoints("100")).toBe(10000);
    expect(parseBasisPoints("100.01")).toBeNull();
    expect(parseBasisPoints("150", { max: 20000 })).toBe(15000);
    expect(parseBasisPoints("250", { max: 20000 })).toBeNull();
  });

  it("rejects a minus sign unless negatives are allowed", () => {
    expect(parseBasisPoints("-2.5")).toBeNull();
    expect(parseBasisPoints("-2.5%", { allowNegative: true })).toBe(-250);
    expect(parseBasisPoints("-101", { allowNegative: true })).toBeNull();
  });

  it("formats, writes and parses in the locale's spelling", () => {
    const de = { locale: "de-DE" };
    expect(formatBasisPoints(825, de)).toBe(`8,25${NBSP}%`);
    expect(basisPointsToInput(825, de)).toBe("8,25");
    expect(parseBasisPoints(`8,25${NBSP}%`, de)).toBe(825);
    expect(parseBasisPoints("8,25", de)).toBe(825);
  });

  it("writes zero as 0 and keeps the round trip exact", () => {
    expect(basisPointsToInput(0)).toBe("0");
    expect(basisPointsToInput(-250)).toBe("-2.5");
    for (const bps of [0, 1, 10, 99, 825, 850, 1000, 9999, 10000]) {
      expect(parseBasisPoints(basisPointsToInput(bps))).toBe(bps);
      expect(parseBasisPoints(formatBasisPoints(bps))).toBe(bps);
    }
  });
});
