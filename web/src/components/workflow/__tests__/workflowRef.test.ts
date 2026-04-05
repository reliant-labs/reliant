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
    it("drops orphaned entry ids and rewrites stale output node refs when a single valid entry remains", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["agent_loop", "router-1775337214113"],
        {
          result: "nodes.agent_loop.output",
          mixed: "nodes.agent_loop.output + nodes.router-1775337214113.output",
        },
        ["router-1775337214113"],
      );

      expect(sanitized.entry).toEqual(["router-1775337214113"]);
      expect(sanitized.outputs).toEqual({
        result: "nodes.router-1775337214113.output",
        mixed:
          "nodes.router-1775337214113.output + nodes.router-1775337214113.output",
      });
    });

    it("preserves existing valid dotted node refs", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["router_ok", "router-123"],
        {
          dottedSafe: "nodes.router_ok.output",
          dottedHyphenated: "nodes.router-123.output",
        },
        ["router_ok", "router-123"],
      );

      expect(sanitized.entry).toEqual(["router_ok", "router-123"]);
      expect(sanitized.outputs).toEqual({
        dottedSafe: "nodes.router_ok.output",
        dottedHyphenated: "nodes.router-123.output",
      });
    });

    it("drops stale output expressions when no single fallback node exists", () => {
      const sanitized = sanitizeWorkflowReferences(
        ["missing-node"],
        {
          result: "nodes.missing-node.output",
          preserved: "inputs.query",
        },
        ["router-a", "router-b"],
      );

      expect(sanitized.entry).toBeUndefined();
      expect(sanitized.outputs).toEqual({
        preserved: "inputs.query",
      });
    });
  });
});