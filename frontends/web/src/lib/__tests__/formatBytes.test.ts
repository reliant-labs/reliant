import { describe, expect, it } from "vitest";
import { formatBytes } from "../formatBytes";

describe("formatBytes", () => {
  it("scales and rounds", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(999)).toBe("999 B");
    expect(formatBytes(1_500)).toBe("1.5 KB");
    expect(formatBytes(2_400_000_000n)).toBe("2.4 GB");
    expect(formatBytes(12_000_000_000)).toBe("12 GB");
    expect(formatBytes(-5)).toBe("0 B");
  });
});
