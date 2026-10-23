// Guards the badge-variant resolution in src/lib/format-utils.ts.
//
// The bug this pins down: an unregistered status used to fall through to a
// SUBSTRING "semantic guess", and "inactive" CONTAINS "active" — so a retired
// patient rendered success-green. The same inversion held for every common
// English negation: incomplete/complete, disabled/enabled, unhealthy/healthy,
// unverified/verified, disconnected/connected. A wrong colour is worse than no
// colour — grey reads "nobody registered this", green reads "this is fine" —
// so the guess is gone and an unknown status is neutral.
//
// Lives under the browser-shared root because it is a Vitest suite: Next.js
// and Vite SPA both run `vitest run`. React Native scaffolds the same
// format-utils.ts but runs jest-expo, which cannot resolve `vitest`.

import { afterEach, describe, expect, it, vi } from "vitest";

import {
  enumBadgeVariant,
  formatAge,
  formatDate,
  formatMoneyCents,
  formatMoneyWhole,
  formatValue,
  timestampToDate,
} from "@/lib/format-utils";

// A protobuf-es v2 numeric enum, carrying both the forward and reverse entries.
enum PatientStatus {
  UNSPECIFIED = 0,
  ACTIVE = 1,
  INACTIVE = 2,
  SENT_TO_PHARMACY = 3,
}

describe("enumBadgeVariant", () => {
  // The measured defect, verbatim: a retired patient rendered as active.
  it("never renders a negated status as success", () => {
    for (const status of [
      "inactive",
      "incomplete",
      "disabled",
      "unhealthy",
      "unverified",
      "disconnected",
    ]) {
      expect(enumBadgeVariant(status)).not.toBe("success");
    }
  });

  it("still renders the affirmative form as success", () => {
    for (const status of [
      "active",
      "completed",
      "enabled",
      "healthy",
      "verified",
      "connected",
    ]) {
      expect(enumBadgeVariant(status)).toBe("success");
    }
  });

  // The inversion also reached the badge through the enum object, which is how
  // every generated page calls it.
  it("does not invert a negated member name resolved from an enum object", () => {
    expect(enumBadgeVariant(PatientStatus.ACTIVE, PatientStatus)).toBe(
      "success",
    );
    expect(enumBadgeVariant(PatientStatus.INACTIVE, PatientStatus)).not.toBe(
      "success",
    );
  });

  it("is neutral for an unset value", () => {
    expect(enumBadgeVariant(null)).toBe("neutral");
    expect(enumBadgeVariant(undefined)).toBe("neutral");
    expect(enumBadgeVariant("")).toBe("neutral");
  });

  // Neutral, not a guess: an unregistered domain status has no derivable
  // colour, and saying so is the honest answer.
  it("is neutral for an unregistered domain status", () => {
    expect(enumBadgeVariant("sent_to_pharmacy")).toBe("neutral");
    expect(enumBadgeVariant("payment_captured")).toBe("neutral");
  });

  // …and registerStatusVariants is where the answer gets DECLARED.
  //
  // Registration mutates module-level state, and nothing un-registers. So the
  // test registers into a FRESH copy of the module (resetModules + a dynamic
  // import) and never touches the one the rest of this file imported. It used
  // to register into the shared copy and rely on running last, which held
  // only until the order changed: under `vitest --sequence.shuffle` the
  // "unregistered domain status" test above failed in 13 of 30 runs, because
  // by then sent_to_pharmacy WAS registered.
  describe("registerStatusVariants", () => {
    afterEach(() => {
      vi.resetModules();
    });

    it("colours a domain status once the project registers it", async () => {
      vi.resetModules();
      const fresh = await import("@/lib/format-utils");
      fresh.registerStatusVariants({
        sent_to_pharmacy: "info",
        capture_failed: "danger", // alias for "error"
        renewal_required: "warning",
      });

      expect(fresh.enumBadgeVariant("sent_to_pharmacy")).toBe("info");
      expect(fresh.enumBadgeVariant("SENT_TO_PHARMACY")).toBe("info");
      expect(
        fresh.enumBadgeVariant(PatientStatus.SENT_TO_PHARMACY, PatientStatus),
      ).toBe("info");
      expect(fresh.enumBadgeVariant("capture_failed")).toBe("error");
      expect(fresh.enumBadgeVariant("renewal_required")).toBe("warning");

      // The isolation is the point, so pin it: the registration above must
      // not have reached the module every other test in this file uses.
      // Without it this line fails in EVERY order, not just unlucky ones.
      expect(enumBadgeVariant("sent_to_pharmacy")).toBe("neutral");
    });
  });
});

// Guards the `bytes` column rendering in src/lib/format-utils.ts.
//
// The bug this pins down: a protobuf-es `bytes` field is a Uint8Array at
// runtime, and formatValue's final `String(value)` turned it into the
// comma-joined byte values — a 3-byte blob rendered as the literal text
// "1,2,3", and a 40 KB thumbnail rendered as a five-figure wall of digits
// that broke the table layout. `bytes` is the ONE proto scalar kind whose
// JS representation has no readable string form, so it is the one kind
// formatValue has to answer for explicitly.
describe("formatValue for a bytes column", () => {
  it("never renders the comma-joined byte values", () => {
    // The measured defect, verbatim.
    expect(formatValue(new Uint8Array([1, 2, 3]))).not.toBe("1,2,3");
    // …and no size string may contain the byte list for ANY blob: the
    // assertion is derived from the value under test, not from a
    // remembered example.
    for (const len of [1, 2, 3, 17, 999, 1000, 4096, 5_000_000]) {
      const blob = new Uint8Array(len).fill(7);
      expect(formatValue(blob)).not.toContain(
        Array.from(blob.slice(0, 3)).join(","),
      );
    }
  });

  it("renders the blob's size", () => {
    expect(formatValue(new Uint8Array(0))).toBe("0 bytes");
    expect(formatValue(new Uint8Array(1))).toBe("1 byte");
    expect(formatValue(new Uint8Array([1, 2, 3]))).toBe("3 bytes");
    expect(formatValue(new Uint8Array(999))).toBe("999 bytes");
    expect(formatValue(new Uint8Array(1200))).toBe("1.2 KB");
    expect(formatValue(new Uint8Array(812_000))).toBe("812 KB");
    expect(formatValue(new Uint8Array(3_400_000))).toBe("3.4 MB");
  });

  // An unset column and a present-but-empty blob are different facts. Only
  // null/undefined are unset; a zero-length blob is a value.
  it("distinguishes an empty blob from an unset column", () => {
    expect(formatValue(new Uint8Array(0))).toBe("0 bytes");
    expect(formatValue(null)).toBe("—");
    expect(formatValue(undefined)).toBe("—");
  });

  // The size must MOVE with the blob — a formatter that returned a constant
  // (or read the wrong property) would satisfy "not 1,2,3" while telling the
  // reader nothing.
  it("reports a size that tracks the blob's length", () => {
    const seen = new Set<string>();
    for (const len of [0, 1, 500, 1500, 250_000, 7_000_000]) {
      seen.add(formatValue(new Uint8Array(len)));
    }
    expect(seen.size).toBe(6);
  });
});

describe("formatMoneyCents with a row's currency", () => {
  // The generated pages pass each row's own currency column. Intl throws on
  // a code that is not ISO 4217 (an empty string, a typo, a seeded
  // `sample_currency_3`), and a throw in a table cell blanked the whole page.
  it("renders an unusable code as the amount beside the raw value", () => {
    expect(() => formatMoneyCents(1899n, "sample_currency_3")).not.toThrow();
    expect(formatMoneyCents(1899n, "sample_currency_3")).toContain(
      "sample_currency_3",
    );
    expect(formatMoneyCents(1899n, "")).toContain("18.99");
    expect(formatMoneyCents(1899n, null)).toContain("18.99");
  });

  it("still formats a real code as currency", () => {
    expect(formatMoneyCents(1899n, "USD")).toBe("$18.99");
  });
});

describe("formatMoneyWhole", () => {
  // The whole point of the second spelling: no cents on a headline figure.
  it("drops the cents that formatMoneyCents keeps", () => {
    expect(formatMoneyWhole(27_200_000n)).toBe("$272,000");
    expect(formatMoneyCents(27_200_000n)).toBe("$272,000.00");
  });

  // Rounding, not truncation — $10.99 is nearer $11 than $10.
  it("rounds to the nearest whole unit", () => {
    expect(formatMoneyWhole(1099)).toBe("$11");
    expect(formatMoneyWhole(1049)).toBe("$10");
  });

  it("renders an unset amount as an em dash, and honours currency", () => {
    expect(formatMoneyWhole(null)).toBe("—");
    expect(formatMoneyWhole(undefined)).toBe("—");
    expect(formatMoneyWhole(0)).toBe("$0");
    expect(formatMoneyWhole(150_000, "EUR")).toContain("1,500");
  });
});

describe("timestampToDate", () => {
  it("converts a proto Timestamp's bigint seconds", () => {
    expect(timestampToDate({ seconds: 1_705_190_400n })?.toISOString()).toBe(
      "2024-01-14T00:00:00.000Z",
    );
  });

  // The guard that keeps "Invalid Date" out of the UI: an out-of-range or
  // unset timestamp is null, which every formatter renders as "—".
  it("returns null rather than an Invalid Date", () => {
    expect(timestampToDate(null)).toBeNull();
    expect(timestampToDate(undefined)).toBeNull();
    expect(timestampToDate({ seconds: 10_000_000_000_000n })).toBeNull();
    expect(formatDate({ seconds: 10_000_000_000_000n })).toBe("—");
  });
});

describe("formatAge", () => {
  const now = new Date("2024-06-15T12:00:00Z");
  const daysAgo = (n: number) => ({
    seconds: BigInt(Math.floor(now.getTime() / 1000) - n * 86_400),
  });

  it("names the recent past instead of counting it", () => {
    expect(formatAge(daysAgo(0), now)).toBe("Today");
    expect(formatAge(daysAgo(1), now)).toBe("Yesterday");
  });

  // The unit coarsens as the magnitude grows: "45 days ago" is harder to read
  // than "6 weeks ago".
  it("coarsens the unit as the age grows", () => {
    expect(formatAge(daysAgo(3), now)).toBe("3 days ago");
    expect(formatAge(daysAgo(45), now)).toBe("6 weeks ago");
    expect(formatAge(daysAgo(120), now)).toBe("4 months ago");
    expect(formatAge(daysAgo(800), now)).toBe("2 years ago");
  });

  it("renders an unset or future timestamp as an em dash", () => {
    expect(formatAge(null, now)).toBe("—");
    expect(formatAge(daysAgo(-5), now)).toBe("—");
  });
});
