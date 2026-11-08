// Copyright (c) 2025 Reliant Labs

/** Sizes in words, which preview an object gets, and a text or JSON preview's text. */

import { describe, expect, it } from "vitest";

import { exactBytes, formatSize, mediaType, previewKindOf, previewText } from "../storageObject-model";

const bytes = (text: string) => new TextEncoder().encode(text);

describe("formatSize", () => {
  it("says a size in the binary units the caps are written in", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(912)).toBe("912 B");
    expect(formatSize(1023)).toBe("1023 B");
    expect(formatSize(1024)).toBe("1 KiB");
    expect(formatSize(1536)).toBe("1.5 KiB");
    expect(formatSize(10 * 1024 * 1024)).toBe("10 MiB");
    expect(formatSize(123_456_789)).toBe("118 MiB");
    expect(formatSize(5 * 1024 ** 4)).toBe("5 TiB");
  });

  it("refuses what is not a size", () => {
    expect(formatSize(-1)).toBe("—");
    expect(formatSize(Number.NaN)).toBe("—");
  });

  it("says the exact count", () => {
    expect(exactBytes(1)).toBe("1 byte");
    expect(exactBytes(1536)).toBe("1,536 bytes");
  });
});

describe("previewKindOf", () => {
  it("takes the server's judgement for text, JSON and none", () => {
    expect(previewKindOf({ preview: "text", contentType: "text/plain" })).toBe("text");
    expect(previewKindOf({ preview: "json", contentType: "application/json" })).toBe("json");
    expect(previewKindOf({ preview: "none", contentType: "application/pdf" })).toBe("none");
  });

  it("draws only the four raster image types, whatever the server says", () => {
    for (const type of ["image/png", "image/jpeg", "image/gif", "image/webp", "IMAGE/PNG; charset=binary"]) {
      expect(previewKindOf({ preview: "image", contentType: type })).toBe("image");
    }
    expect(previewKindOf({ preview: "image", contentType: "image/svg+xml" })).toBe("none");
    expect(previewKindOf({ preview: "image", contentType: "text/html" })).toBe("none");
    expect(previewKindOf({ preview: "image", contentType: "" })).toBe("none");
    expect(mediaType(" Image/WebP ; q=1")).toBe("image/webp");
  });
});

describe("previewText", () => {
  it.each(['{"a":[1,2],"b":{"c":null}}', '{"id":9007199254740993,"decimal":0.1234567890123456789,"exponent":1e999,"id":2}'])("preserves JSON source exactly: %s", (text) => {
    const content = bytes(text);
    expect(previewText(content, "json", content.byteLength)).toEqual({ text, pretty: false, cut: false, unparsed: false });
  });

  it("shows JSON that does not parse as stored", () => {
    const content = bytes('{"a": [1,, }');
    expect(previewText(content, "json", content.byteLength)).toEqual({ text: '{"a": [1,, }', pretty: false, cut: false, unparsed: true });
  });

  it("says when the server cut the object, and does not try to parse a cut JSON document", () => {
    const content = bytes('{"rows": [1, 2, 3');
    expect(previewText(content, "json", 3 * 1024 * 1024)).toMatchObject({ pretty: false, cut: true, unparsed: true });
    expect(previewText(bytes("line\n"), "text", 3 * 1024 * 1024)).toEqual({ text: "line\n", pretty: false, cut: true, unparsed: false });
  });

  it("decodes UTF-8, replacing invalid sequences, and never reads markup", () => {
    const shown = previewText(new Uint8Array([0x68, 0x69, 0xff, 0x3c, 0x62, 0x3e]), "text", 6);
    expect(shown.text).toBe("hi\uFFFD<b>");
    expect(previewText(bytes("ünïcødé ✓"), "text", bytes("ünïcødé ✓").byteLength).text).toBe("ünïcødé ✓");
  });
});
