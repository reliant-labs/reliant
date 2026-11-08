// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser's URL state: what a link means as a ListObjects
 * request, what a change keeps, how a prefix becomes a breadcrumb (keys with
 * odd characters included), and the way back through opaque page tokens.
 */

import { describe, expect, it } from "vitest";
import { defaultParseSearch } from "@tanstack/react-router";
import { forgeConsoleStorageSearchSchema } from "@/routeSchemas";
import { withSearch } from "../../Console/paths";

import {
  basename,
  breadcrumbsOf,
  entryName,
  folderOf,
  isBucketName,
  listRequestOf,
  objectOf,
  pageNumber,
  parentPrefix,
  patchStorageSearch,
  pickStorageSearch,
  prefixOf,
  previousToken,
  shownSegment,
  tokenOf,
  trailAfterNext,
  trailWith,
} from "../storageUrl-model";

const CTX = { environmentId: "env-prod", bucket: "uploads" };

describe("URL → ListObjects request", () => {
  it("round-trips odd keys through the router's encoding and validates bounds", () => {
    const state = { env: "prod", bucket: "uploads", prefix: "odd names/%2F//", object: "odd names/  ü#?&%.txt", token: "a+/=?&" };
    const url = withSearch("/forge/p/shop/storage", state);
    expect(forgeConsoleStorageSearchSchema.parse(defaultParseSearch(url.slice(url.indexOf("?"))))).toEqual(state);
    expect(forgeConsoleStorageSearchSchema.parse({ bucket: "../bad", prefix: "é".repeat(513), object: {}, token: "x".repeat(4097) })).toEqual({
      bucket: undefined, prefix: undefined, object: undefined, token: undefined,
    });
    expect(forgeConsoleStorageSearchSchema.parse({ object: 2026 })).toEqual({ object: "2026" });
  });

  it("lists the root's first page of a bare link", () => {
    expect(listRequestOf({}, CTX)).toEqual({ environmentId: "env-prod", bucket: "uploads", prefix: "", pageToken: "", pageSize: 0 });
  });

  it("carries the level, the page and the confirmation, and never decides open_bucket", () => {
    const access = { confirmEnvId: "env-prod", reason: "checking an avatar" };
    const req = listRequestOf({ bucket: "uploads", prefix: "avatars/2026/", token: "t-2", object: "avatars/2026/ada.png" }, { ...CTX, access, pageSize: 50 });
    expect(req).toEqual({ environmentId: "env-prod", bucket: "uploads", prefix: "avatars/2026/", pageToken: "t-2", pageSize: 50, access });
    expect(req).not.toHaveProperty("openBucket");
  });

  it("passes a key with odd characters through untouched", () => {
    const prefix = "odd names/100% done #1?/ünï//";
    expect(listRequestOf({ prefix }, CTX).prefix).toBe(prefix);
  });

  it("reads a value the server would refuse as absent", () => {
    // 1024 bytes is the limit: 512 two-byte characters fit, one more does not.
    expect(prefixOf({ prefix: "é".repeat(512) })).toBe("é".repeat(512));
    expect(prefixOf({ prefix: "é".repeat(513) })).toBe("");
    expect(objectOf({ object: "a".repeat(1025) })).toBeNull();
    expect(objectOf({ object: "" })).toBeNull();
    expect(objectOf({ object: "docs/readme.txt" })).toBe("docs/readme.txt");
    expect(tokenOf({ token: "x".repeat(4097) })).toBe("");
    expect(isBucketName("uploads")).toBe(true);
    expect(isBucketName("Uploads")).toBe(false);
    expect(isBucketName("../etc")).toBe(false);
    expect(isBucketName(undefined)).toBe(false);
  });

  it("picks only the storage keys of a route's search, as strings", () => {
    expect(pickStorageSearch({ env: "prod", bucket: "uploads", prefix: "a/", object: "", token: 7, other: "x" })).toEqual({ bucket: "uploads", prefix: "a/" });
  });
});

describe("patchStorageSearch", () => {
  const open = { bucket: "uploads", prefix: "avatars/", object: "avatars/x.png", token: "t-2" };

  it("drops the level, object and page on another bucket", () => {
    expect(patchStorageSearch(open, { bucket: "exports" })).toEqual({ bucket: "exports" });
  });

  it("drops the object and page on another level: the token is sealed to its prefix", () => {
    expect(patchStorageSearch(open, { prefix: "avatars/2026/" })).toEqual({ bucket: "uploads", prefix: "avatars/2026/" });
    expect(patchStorageSearch(open, { prefix: undefined })).toEqual({ bucket: "uploads" });
  });

  it("keeps the open object while paging, and the page while opening an object", () => {
    expect(patchStorageSearch(open, { token: "t-3" })).toEqual({ ...open, token: "t-3" });
    expect(patchStorageSearch(open, { object: "avatars/y.png" })).toEqual({ ...open, object: "avatars/y.png" });
    expect(patchStorageSearch(open, { object: undefined })).toEqual({ bucket: "uploads", prefix: "avatars/", token: "t-2" });
  });

  it("keeps everything when the bucket or prefix is set to what it already is", () => {
    expect(patchStorageSearch(open, { bucket: "uploads", prefix: "avatars/" })).toEqual(open);
  });

  it("drops empty values", () => {
    expect(patchStorageSearch({ bucket: "uploads" }, { prefix: "", token: "" })).toEqual({ bucket: "uploads" });
  });
});

describe("names and the breadcrumb", () => {
  it("is one crumb per segment, each listing the prefix up to it", () => {
    expect(breadcrumbsOf("")).toEqual([]);
    expect(breadcrumbsOf("avatars/2026/")).toEqual([
      { segment: "avatars", prefix: "avatars/", partial: false },
      { segment: "2026", prefix: "avatars/2026/", partial: false },
    ]);
  });

  it("keeps an empty segment, a leading slash and odd characters as stored", () => {
    expect(breadcrumbsOf("a//b/")).toEqual([
      { segment: "a", prefix: "a/", partial: false },
      { segment: "", prefix: "a//", partial: false },
      { segment: "b", prefix: "a//b/", partial: false },
    ]);
    expect(breadcrumbsOf("/x/").map((crumb) => crumb.prefix)).toEqual(["/", "/x/"]);
    expect(breadcrumbsOf("odd names/100% done #1?/").map((crumb) => crumb.segment)).toEqual(["odd names", "100% done #1?"]);
    expect(breadcrumbsOf("  spaced /").map((crumb) => crumb.segment)).toEqual(["  spaced "]);
    expect(shownSegment("")).toBe("(empty)");
    expect(shownSegment(" x ")).toBe(" x ");
  });

  it("ends a typed narrowing in a partial crumb", () => {
    expect(breadcrumbsOf("docs/re")).toEqual([
      { segment: "docs", prefix: "docs/", partial: false },
      { segment: "re", prefix: "docs/re", partial: true },
    ]);
    expect(breadcrumbsOf("re")).toEqual([{ segment: "re", prefix: "re", partial: true }]);
  });

  it("names entries relative to the folder listed, a narrowing's too", () => {
    expect(entryName("avatars/", "avatars/2026/")).toBe("2026");
    expect(entryName("avatars/", "avatars/legacy.jpg")).toBe("legacy.jpg");
    expect(entryName("docs/re", "docs/readme.txt")).toBe("readme.txt");
    expect(entryName("", "robots.txt")).toBe("robots.txt");
    expect(entryName("a/", "a//")).toBe("");
    expect(folderOf("docs/re")).toBe("docs/");
    expect(folderOf("docs/")).toBe("docs/");
    expect(folderOf("re")).toBe("");
  });

  it("finds an object's level and file name", () => {
    expect(parentPrefix("avatars/2026/ada.png")).toBe("avatars/2026/");
    expect(parentPrefix("robots.txt")).toBe("");
    expect(basename("avatars/2026/ada.png")).toBe("ada.png");
    expect(basename("odd names/100% done #1?.txt")).toBe("100% done #1?.txt");
    expect(basename("folder-marker/")).toBe("");
  });
});

describe("paging by opaque token", () => {
  it("walks forward, and back through the pages it walked", () => {
    let trail = trailWith([], "");
    expect(pageNumber(trail, "")).toBe(1);
    expect(previousToken(trail, "")).toBeNull();
    trail = trailAfterNext(trail, "", "t-2");
    trail = trailAfterNext(trail, "t-2", "t-3");
    expect(trail).toEqual(["", "t-2", "t-3"]);
    expect(pageNumber(trail, "t-3")).toBe(3);
    expect(previousToken(trail, "t-3")).toBe("t-2");
    expect(previousToken(trail, "t-2")).toBe("");
  });

  it("forgets the pages after the one it pages forward from", () => {
    expect(trailAfterNext(["", "t-2", "t-3"], "t-2", "t-3b")).toEqual(["", "t-2", "t-3b"]);
  });

  it("knows no way back, and no page number, from a deep link to a later page", () => {
    const trail = trailWith(["", "t-2"], "t-9");
    expect(trail).toEqual(["t-9"]);
    expect(previousToken(trail, "t-9")).toBeNull();
    expect(pageNumber(trail, "t-9")).toBeNull();
    expect(pageNumber(trailAfterNext(trail, "t-9", "t-10"), "t-10")).toBeNull();
  });
});
