import { afterEach, describe, expect, it } from "vitest";

import { markNoIndex } from "../noindex";

const robots = () => document.head.querySelectorAll('meta[name="robots"]');

afterEach(() => {
  robots().forEach((m) => m.remove());
});

describe("markNoIndex", () => {
  it("adds one noindex robots tag", () => {
    expect(robots()).toHaveLength(0);
    markNoIndex();
    markNoIndex();
    expect(robots()).toHaveLength(1);
    expect((robots()[0] as HTMLMetaElement).content).toBe("noindex, nofollow");
  });

  it("overrides a robots tag the page already has", () => {
    const meta = document.createElement("meta");
    meta.name = "robots";
    meta.content = "index, follow";
    document.head.append(meta);
    expect(markNoIndex()).toBe(meta);
    expect(robots()).toHaveLength(1);
    expect(meta.content).toBe("noindex, nofollow");
  });
});
