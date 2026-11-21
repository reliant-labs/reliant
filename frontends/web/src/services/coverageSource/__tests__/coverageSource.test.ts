// Copyright (c) 2026 Reliant Labs

/**
 * The source seam's guard: text is drawn only when it IS the blob the report
 * names (git's own blob id, computed here), and the real fetch, until
 * GetCoverageSource exists, says so.
 */

import { describe, expect, it } from "vitest";

import { FIXTURE_REFUND_BLOB, FIXTURE_REFUND_SOURCE } from "@/components/Forge/Tests/coverageFixtures";

import { fetchCoverageSource, gitBlobId, sha1Hex, sourceLineCount, verifySource } from "../coverageSource";

describe("git blob ids", () => {
  it("SHA-1 matches the standard vectors", () => {
    const utf8 = (text: string) => new TextEncoder().encode(text);
    expect(sha1Hex(utf8(""))).toBe("da39a3ee5e6b4b0d3255bfef95601890afd80709");
    expect(sha1Hex(utf8("abc"))).toBe("a9993e364706816aba3e25717850c26c9cd0d89d");
    expect(sha1Hex(utf8("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"))).toBe("84983e441c3bd26ebaae4aa1f95129e5e54670f1");
    expect(sha1Hex(utf8("a".repeat(1000)))).toBe("291e9a6c66994949b57ba5e650361e98fc36b1ba");
  });

  it("is what git hash-object prints", () => {
    expect(gitBlobId("")).toBe("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391");
    expect(gitBlobId("hello\n")).toBe("ce013625030ba8dba906f756967f9e9ca394464a");
    // Byte length, not character length: a non-ASCII file.
    expect(gitBlobId("caf\u00e9\n")).toBe("572eb43fe8e34fb87d01c69e01151ff696022924");
  });

  it("the fixture's blob id is its source's", () => {
    expect(gitBlobId(FIXTURE_REFUND_SOURCE)).toBe(FIXTURE_REFUND_BLOB);
  });
});

describe("verifySource", () => {
  it("passes the exact blob, in either case of hex", () => {
    expect(verifySource({ text: FIXTURE_REFUND_SOURCE }, FIXTURE_REFUND_BLOB.toUpperCase(), 34)).toEqual({ text: FIXTURE_REFUND_SOURCE });
  });

  it("refuses one changed byte, a missing blob id, or a file too short for the lines the report covers", () => {
    expect(verifySource({ text: `${FIXTURE_REFUND_SOURCE} ` }, FIXTURE_REFUND_BLOB, 34)).toEqual({ unavailable: "mismatch" });
    expect(verifySource({ text: FIXTURE_REFUND_SOURCE }, "", 34)).toEqual({ unavailable: "mismatch" });
    expect(verifySource({ text: FIXTURE_REFUND_SOURCE }, FIXTURE_REFUND_BLOB, 37)).toEqual({ unavailable: "mismatch" });
  });

  it("keeps a reason as it is", () => {
    expect(verifySource({ unavailable: "revoked" }, FIXTURE_REFUND_BLOB, 1)).toEqual({ unavailable: "revoked" });
  });

  it("counts lines as a viewer numbers them", () => {
    expect(sourceLineCount("")).toBe(0);
    expect(sourceLineCount("a")).toBe(1);
    expect(sourceLineCount("a\n")).toBe(1);
    expect(sourceLineCount("a\nb")).toBe(2);
    expect(sourceLineCount(FIXTURE_REFUND_SOURCE)).toBe(36);
  });
});

describe("the real fetch", () => {
  it("answers not-yet-available until GetCoverageSource exists, and uncommitted with no blob", async () => {
    await expect(fetchCoverageSource("shopfront", FIXTURE_REFUND_BLOB)).resolves.toEqual({ unavailable: "not-yet-available" });
    await expect(fetchCoverageSource("shopfront", "")).resolves.toEqual({ unavailable: "uncommitted" });
  });
});
