// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// useInteractionEnd. React is stubbed rather than rendered, as in
// service-hooks.test.ts — this package carries no DOM renderer — and the stub
// runs each effect when its dependencies change, which is the property the
// hook relies on.
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Interaction } from "./interaction.js";

let lastDeps: unknown[] | undefined;

vi.mock("react", () => ({
  useEffect: (fn: () => void, deps?: unknown[]) => {
    const changed =
      !lastDeps ||
      !deps ||
      deps.length !== lastDeps.length ||
      deps.some((d, i) => !Object.is(d, lastDeps![i]));
    lastDeps = deps;
    if (changed) fn();
  },
}));

const { useInteractionEnd } = await import("./interaction-hooks.js");

type FakeOp = Interaction & {
  ended: boolean;
  endAfterPaint: ReturnType<typeof vi.fn>;
};

function fakeOp(): FakeOp {
  const op: FakeOp = {
    name: "chat.open",
    scope: "foreground",
    traceId: "t",
    ended: false,
    setAttributes: vi.fn(),
    mark: vi.fn(),
    end: vi.fn(),
    run: <T>(fn: () => T) => fn(),
    endAfterPaint: vi.fn(() => {
      op.ended = true;
    }),
  };
  return op;
}

describe("useInteractionEnd", () => {
  beforeEach(() => {
    lastDeps = undefined;
  });

  it("ends after paint on the first ready commit, once", () => {
    const op = fakeOp();
    useInteractionEnd(op, false);
    expect(op.endAfterPaint).not.toHaveBeenCalled();
    useInteractionEnd(op, true, { outcome: "ok", "chat.messages": 3 });
    expect(op.endAfterPaint).toHaveBeenCalledExactlyOnceWith({
      outcome: "ok",
      "chat.messages": 3,
    });
    useInteractionEnd(op, true, { outcome: "error" });
    expect(op.endAfterPaint).toHaveBeenCalledOnce();
  });

  it("ignores a missing or already-ended interaction", () => {
    useInteractionEnd(null, true);
    const op = fakeOp();
    op.ended = true;
    useInteractionEnd(op, true);
    expect(op.endAfterPaint).not.toHaveBeenCalled();
  });
});
