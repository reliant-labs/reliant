import { describe, expect, it } from "vitest";
import { linkifySegments } from "./linkify";

const links = (text: string) =>
  linkifySegments(text)
    .filter((s) => s.type === "link")
    .map((s) => s.text);

describe("linkifySegments", () => {
  it("leaves text with no URL as one text run", () => {
    expect(linkifySegments("Which database?")).toEqual([{ type: "text", text: "Which database?" }]);
  });

  it("splits text around an http(s) URL, keeping every character", () => {
    const text = "See https://example.com/a?b=1#c and http://x.io too";
    const segments = linkifySegments(text);
    expect(segments).toEqual([
      { type: "text", text: "See " },
      { type: "link", text: "https://example.com/a?b=1#c", href: "https://example.com/a?b=1#c" },
      { type: "text", text: " and " },
      { type: "link", text: "http://x.io", href: "http://x.io" },
      { type: "text", text: " too" },
    ]);
    expect(segments.map((s) => s.text).join("")).toBe(text);
  });

  it("leaves sentence punctuation after a URL as text", () => {
    expect(links("Open https://example.com/pr/1.")).toEqual(["https://example.com/pr/1"]);
    expect(links("https://example.com, then https://b.dev!")).toEqual(["https://example.com", "https://b.dev"]);
    expect(links("is it https://example.com/x?")).toEqual(["https://example.com/x"]);
    expect(links("see https://example.com/x...")).toEqual(["https://example.com/x"]);
  });

  it("drops a closing bracket the URL did not open, keeps one it did", () => {
    expect(links("(see https://example.com/docs)")).toEqual(["https://example.com/docs"]);
    expect(links("(https://example.com/docs).")).toEqual(["https://example.com/docs"]);
    expect(links("[https://example.com]")).toEqual(["https://example.com"]);
    expect(links("https://en.wikipedia.org/wiki/Go_(game)")).toEqual(["https://en.wikipedia.org/wiki/Go_(game)"]);
  });

  it("stops at quotes and angle brackets", () => {
    expect(links('the "https://example.com" link')).toEqual(["https://example.com"]);
    expect(links("<https://example.com>")).toEqual(["https://example.com"]);
  });

  it("links only explicit http(s) URLs with a host", () => {
    expect(links("example.com or localhost:3000 or ftp://x.io")).toEqual([]);
    expect(links("just https:// alone")).toEqual([]);
    expect(links("xhttps://example.com")).toEqual([]);
  });
});
