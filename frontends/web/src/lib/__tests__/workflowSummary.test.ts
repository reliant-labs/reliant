import { describe, expect, it } from "vitest";
import { descriptionHasMore, workflowSummary } from "../workflowSummary";

describe("workflowSummary", () => {
  it("is the first sentence of the first paragraph", () => {
    const description = `An independent reviewer for every non-draft pull request in the repositories you enable it for. It reads
  the diff and posts ONE review (node \`decide\`).

  Reviews a PR when it is opened.`;
    expect(workflowSummary(description)).toBe(
      "An independent reviewer for every non-draft pull request in the repositories you enable it for.",
    );
    expect(descriptionHasMore(description)).toBe(true);
  });

  it("does not end a sentence inside an abbreviation or a version", () => {
    expect(workflowSummary("Reads e.g. issues from v1.2 onward. Then more.")).toBe("Reads e.g. issues from v1.2 onward.");
  });

  it("keeps a short description whole, and bounds a long one at a word", () => {
    expect(workflowSummary("Triage new issues")).toBe("Triage new issues");
    expect(descriptionHasMore("Triage new issues")).toBe(false);
    const long = workflowSummary(`${"word ".repeat(60)}end.`);
    expect(long.length).toBeLessThanOrEqual(161);
    expect(long.endsWith("…")).toBe(true);
    expect(workflowSummary(undefined)).toBe("");
  });
});
