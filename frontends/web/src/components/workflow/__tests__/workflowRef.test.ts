import {
  canonicalizeBuiltinWorkflowRef,
  deriveWorkflowEntryFromEdges,
  sanitizeWorkflowReferences,
} from "../workflowRef";

describe("workflowRef helpers", () => {
  describe("deriveWorkflowEntryFromEdges", () => {
    it("prefers workflow start edges over stale explicit entry state", () => {
      const entry = deriveWorkflowEntryFromEdges("old-entry", [
        { source: "workflow", target: "new-entry" },
      ]);

      expect(entry).toEqual(["new-entry"]);
    });

    it("falls back to explicit entry when there are no workflow start edges", () => {
      const entry = deriveWorkflowEntryFromEdges(["alpha", "beta"], []);
      expect(entry).toEqual(["alpha", "beta"]);
    });
  });

  describe("canonicalizeBuiltinWorkflowRef", () => {
    it("canonicalizes bare builtin names when a builtin match exists", () => {
      const result = canonicalizeBuiltinWorkflowRef("agent", [
        "builtin://agent",
        "builtin://structured-agent",
      ]);

      expect(result).toBe("builtin://agent");
    });

    it("preserves refs that already include a protocol or CEL expression", () => {
      expect(
        canonicalizeBuiltinWorkflowRef("builtin://agent", ["builtin://agent"]),
      ).toBe("builtin://agent");
      expect(
        canonicalizeBuiltinWorkflowRef("{{inputs.workflow_ref}}", ["builtin://agent"]),
      ).toBe("{{inputs.workflow_ref}}");
    });
  });

  describe("sanitizeWorkflowReferences", () => {
    it("drops orphaned entry ids and drops stale output node refs", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["agent_loop", "router_1775337214113"],
        {
          result: "nodes.agent_loop.output",
          mixed: "nodes.agent_loop.output + nodes.router_1775337214113.output",
          valid: "nodes.router_1775337214113.output",
        },
        ["router_1775337214113"],
      );

      expect(sanitized.entry).toEqual(["router_1775337214113"]);
      // Stale refs are dropped, not rewritten — the replacement node may have different output fields
      expect(sanitized.outputs).toEqual({
        valid: "nodes.router_1775337214113.output",
      });
    });

    it("preserves existing valid dotted node refs", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["router_ok", "router_123"],
        {
          dottedSafe: "nodes.router_ok.output",
          dottedSafeNumbered: "nodes.router_123.output",
        },
        ["router_ok", "router_123"],
      );

      expect(sanitized.entry).toEqual(["router_ok", "router_123"]);
      expect(sanitized.outputs).toEqual({
        dottedSafe: "nodes.router_ok.output",
        dottedSafeNumbered: "nodes.router_123.output",
      });
    });

    it("drops stale output expressions when no single fallback node exists", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["missing_node"],
        {
          result: "nodes.missing_node.output",
          preserved: "inputs.query",
        },
        ["router_a", "router_b"],
      );

      expect(sanitized.entry).toBeUndefined();
      expect(sanitized.outputs).toEqual({
        preserved: "inputs.query",
      });
    });
  });
});