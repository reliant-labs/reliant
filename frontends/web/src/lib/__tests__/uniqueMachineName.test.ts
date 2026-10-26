import { describe, expect, it } from "vitest";

import { normalizeMachineName, uniqueMachineName } from "../uniqueMachineName";

describe("uniqueMachineName", () => {
  it("uses the base when nothing holds it", () => {
    expect(uniqueMachineName("cloud-small", ["other"])).toBe("cloud-small");
  });

  it("takes the first free suffix", () => {
    expect(uniqueMachineName("cloud-small", ["cloud-small", "cloud-small-2", undefined])).toBe(
      "cloud-small-3",
    );
  });

  it("compares case-insensitively, as the server's name rule is lowercase", () => {
    expect(uniqueMachineName("cloud-small", ["Cloud-Small"])).toBe("cloud-small-2");
  });

  it("normalizes a free-text base into a valid name", () => {
    expect(uniqueMachineName("My Repo!", [])).toBe("my-repo");
    expect(uniqueMachineName("---", [])).toBe("machine");
  });
});

describe("normalizeMachineName", () => {
  it("leaves room for a suffix within 63 characters", () => {
    const name = normalizeMachineName("a".repeat(100));
    expect(name.length).toBeLessThanOrEqual(59);
  });
});
