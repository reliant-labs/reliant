import { describe, expect, it } from "vitest";
import { ProviderPolicy_OnLimit } from "../../gen/reliant/v1/chat_pb";
import { isNewerSelection, selectionFromPayload } from "../chatSelection";

describe("selectionFromPayload", () => {
  it("reads the stream's snake_case payload into a ChatSelection", () => {
    const selection = selectionFromPayload({
      workflow_name: "builtin://agent",
      params: { model: { id: "a" } },
      provider_policy: { on_limit: "fallback", fallback_model: "gpt-5.6-sol@codex" },
      version: 7,
      updated_at: "2026-10-19T00:00:00Z",
    });
    expect(selection.workflowName).toBe("builtin://agent");
    expect(selection.params).toEqual({ model: { id: "a" } });
    expect(selection.version).toBe(7n);
    expect(selection.providerPolicy?.onLimit).toBe(ProviderPolicy_OnLimit.FALLBACK);
    expect(selection.providerPolicy?.fallbackModel).toBe("gpt-5.6-sol@codex");
  });

  it("treats an absent policy as inherit", () => {
    const selection = selectionFromPayload({ workflow_name: "w", params: {}, version: "2" });
    expect(selection.providerPolicy?.onLimit).toBe(ProviderPolicy_OnLimit.UNSPECIFIED);
    expect(selection.version).toBe(2n);
  });
});

describe("isNewerSelection", () => {
  const at = (version: number) => selectionFromPayload({ workflow_name: "w", params: {}, version });

  it("moves forward only: a late event never rolls the composer back", () => {
    expect(isNewerSelection(at(3), at(2))).toBe(false);
    expect(isNewerSelection(at(3), at(3))).toBe(true);
    expect(isNewerSelection(at(3), at(4))).toBe(true);
  });

  it("accepts anything when nothing is cached", () => {
    expect(isNewerSelection(undefined, at(1))).toBe(true);
    expect(isNewerSelection(null, at(1))).toBe(true);
  });
});
