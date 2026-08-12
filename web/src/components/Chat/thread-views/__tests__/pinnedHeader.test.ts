import { describe, expect, it } from "vitest";

// The pinned header shows the user message that heads the section you are
// currently reading. It is resolved through a POSITIONAL map: flatItems index
// -> index of the user message heading that item's section.
//
// This mirrors InterleavedTimeline's userMessageForItem construction.
function buildUserMessageForItem(items: Array<{ role: "user" | "assistant" }>) {
  const mapping: (number | null)[] = [];
  let current: number | null = null;
  for (let i = 0; i < items.length; i++) {
    if (items[i].role === "user") current = i;
    mapping.push(current);
  }
  return mapping;
}

function pinnedFor(mapping: (number | null)[], firstVisible: number) {
  const layer = mapping[firstVisible] ?? null;
  return layer !== null && layer < firstVisible ? layer : null;
}

describe("pinned header index", () => {
  it("names the user message heading the visible section", () => {
    // u0, a1, a2, u3, a4  — viewport starts at a4, whose section is headed by u3
    const items = [
      { role: "user" as const }, { role: "assistant" as const },
      { role: "assistant" as const }, { role: "user" as const },
      { role: "assistant" as const },
    ];
    expect(pinnedFor(buildUserMessageForItem(items), 4)).toBe(3);
  });

  // The regression: the mapping is positional, so anything inserted ABOVE the
  // viewport shifts every index. If the pinned index is only recomputed when
  // the user scrolls (Virtuoso's rangeChanged), a reply arriving while the user
  // sits still leaves the stale index pointing at a different message — the
  // reported "wrong message pinned at the header", with no branching or thread
  // switching required to reproduce.
  it("must be recomputed after rows are inserted above the viewport", () => {
    const before = [
      { role: "user" as const },      // 0
      { role: "assistant" as const }, // 1
      { role: "user" as const },      // 2  <- heads the visible section
      { role: "assistant" as const }, // 3  <- first visible
    ];
    const pinnedBefore = pinnedFor(buildUserMessageForItem(before), 3);
    expect(pinnedBefore).toBe(2);

    // Two rows stream in above the viewport; the same message is now at 5, and
    // its heading user message at 4.
    const after = [
      { role: "user" as const },      // 0
      { role: "assistant" as const }, // 1
      { role: "assistant" as const }, // 2  <- inserted
      { role: "assistant" as const }, // 3  <- inserted
      { role: "user" as const },      // 4  <- same user message, shifted
      { role: "assistant" as const }, // 5  <- same first-visible item, shifted
    ];
    const mappingAfter = buildUserMessageForItem(after);

    // Reusing the pre-insertion index against the new mapping is the bug: index
    // 3 is now an assistant message in the FIRST section, so the header would
    // show the wrong user message.
    expect(mappingAfter[3]).toBe(0);
    expect(mappingAfter[3]).not.toBe(pinnedBefore);

    // Recomputing against the shifted first-visible row is correct.
    expect(pinnedFor(mappingAfter, 5)).toBe(4);
  });

  it("pins nothing when the section head is itself visible", () => {
    const items = [
      { role: "user" as const }, { role: "assistant" as const },
    ];
    // Viewport starts at the user message: it is on screen, so no pin needed.
    expect(pinnedFor(buildUserMessageForItem(items), 0)).toBeNull();
  });
});
