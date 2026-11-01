import { describe, expect, it } from "vitest";
import { addRational, compareRational, divideRational, exactCentsLabel, exactHours, formatRational, parseRational, rational } from "../exact-rational";
describe("exact wire rational quantities", () => {
  it("rejects missing or malformed values, never inventing zero", () => {
    for (const value of [undefined, "", "NaN", "Infinity", "1/0", "1/-2", "-1", "1e3", " 1", "1/2x", "1.2.3"]) expect(parseRational(value)).toBeNull();
    expect(parseRational("0/9")).toEqual(rational(0n));
  });
  it("keeps huge integers and fractional exhaustion exact", () => {
    const a = parseRational("900719925474099312345/7")!;
    expect(compareRational(a, parseRational("900719925474099312344/7")!)).toBe(1);
    expect(addRational(parseRational("1/3")!, parseRational("2/3")!)).toEqual(rational(1n));
    expect(divideRational(a, rational(0n)!)).toBeNull();
  });
  it("rounds only the final display, with a floor option for equivalent hours", () => {
    expect(exactHours("3601/3")).toBe("0.3");
    expect(exactCentsLabel("1/3")).toBe("$0.00");
    expect(exactCentsLabel("115/1")).toBe("$1.15");
    expect(exactCentsLabel("NaN")).toBeNull();
    expect(formatRational(parseRational("299/100")!, 1, "floor")).toBe("2.9");
    expect(formatRational(parseRational("299/100")!, 1)).toBe("3.0");
  });
});
