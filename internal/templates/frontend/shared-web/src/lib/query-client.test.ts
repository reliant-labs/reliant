// Guards the bigint-safe query-key hasher in src/lib/query-client.ts.
//
// The bug this pins down: React Query's default hasher is
// `JSON.stringify(key, sortPlainObjectKeys)`, and `JSON.stringify` THROWS
// `TypeError: Do not know how to serialize a BigInt`. protobuf-es models every
// proto `int64`/`uint64` — `google.protobuf.Timestamp.seconds` included — as a
// JS `bigint`, so any key carrying a `created_at`, a cursor or an int64 id
// would throw before the query ever ran.
//
// Lives under the browser-shared root because it is a Vitest suite: Next.js
// and Vite SPA both run `vitest run`. React Native scaffolds the same
// query-client.ts but runs jest-expo, which cannot resolve `vitest`.

import { hashKey } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { bigintSafeHashKey } from "@/lib/query-client";

describe("bigintSafeHashKey", () => {
  it("hashes a key carrying a bigint that the default hasher rejects", () => {
    // A generated list key with a proto Timestamp filter.
    const key = [
      "posts",
      "list",
      { createdAfter: { seconds: 1721800000n, nanos: 0 }, limit: 50 },
    ];

    expect(() => hashKey(key)).toThrow(TypeError);
    expect(() => bigintSafeHashKey(key)).not.toThrow();
    expect(bigintSafeHashKey(key)).toContain("1721800000");
  });

  it("keeps distinct keys distinct", () => {
    const hashes = [
      bigintSafeHashKey([1n]),
      bigintSafeHashKey([2n]),
      bigintSafeHashKey(["1"]),
      bigintSafeHashKey([1]),
      // The literal string a naive `${value}n` tag would collide with.
      bigintSafeHashKey(["1n"]),
      // The literal string this encoding's own tag would collide with if it
      // did not escape NUL-leading strings.
      bigintSafeHashKey(["\u0000bigint:1"]),
      bigintSafeHashKey(["post", { id: 1n }]),
      bigintSafeHashKey(["post", { id: 2n }]),
    ];

    expect(new Set(hashes).size).toBe(hashes.length);
  });

  it("stays stable across object-key ordering, like the default hasher", () => {
    expect(bigintSafeHashKey([{ a: 1n, b: 2n }])).toBe(
      bigintSafeHashKey([{ b: 2n, a: 1n }]),
    );
    // Two different bigint assignments to the same fields must NOT collapse.
    expect(bigintSafeHashKey([{ a: 1n, b: 2n }])).not.toBe(
      bigintSafeHashKey([{ a: 2n, b: 1n }]),
    );
  });

  it("matches React Query's default hash for bigint-free keys", () => {
    const key = ["posts", "list", { limit: 50, cursor: "abc", open: true }];
    expect(bigintSafeHashKey(key)).toBe(hashKey(key));
  });
});
