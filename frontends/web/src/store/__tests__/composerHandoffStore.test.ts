import { beforeEach, describe, expect, it, vi } from "vitest";

import { HANDOFF_TTL_MS, useComposerHandoffStore } from "../composerHandoffStore";

const offer = (overrides: Partial<Parameters<ReturnType<typeof useComposerHandoffStore.getState>["offer"]>[0]> = {}) =>
  useComposerHandoffStore.getState().offer({ projectId: "p1", worktreeId: "w1", text: "look at ISS-1", ...overrides });

describe("composerHandoffStore", () => {
  beforeEach(() => useComposerHandoffStore.setState({ pending: null }));

  it("hands the offer to the project it is for, once", () => {
    offer();
    const taken = useComposerHandoffStore.getState().take("p1");
    expect(taken).toMatchObject({ projectId: "p1", worktreeId: "w1", text: "look at ISS-1" });
    expect(useComposerHandoffStore.getState().take("p1")).toBeNull();
  });

  it("gives every offer its own increasing id, so the composer applies each once", () => {
    offer();
    const first = useComposerHandoffStore.getState().take("p1")!;
    offer();
    const second = useComposerHandoffStore.getState().take("p1")!;
    expect(second.id).toBeGreaterThan(first.id);
  });

  it("keeps an offer for another project untouched", () => {
    offer({ projectId: "other" });
    expect(useComposerHandoffStore.getState().take("p1")).toBeNull();
    expect(useComposerHandoffStore.getState().pending?.projectId).toBe("other");
  });

  it("drops an offer the navigation never delivered", () => {
    vi.useFakeTimers();
    try {
      offer();
      const later = Date.now() + HANDOFF_TTL_MS + 1;
      expect(useComposerHandoffStore.getState().take("p1", later)).toBeNull();
      expect(useComposerHandoffStore.getState().pending).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("replaces an earlier offer with a newer one", () => {
    offer({ text: "first" });
    offer({ text: "second" });
    expect(useComposerHandoffStore.getState().take("p1")?.text).toBe("second");
  });
});
