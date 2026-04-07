import type { Edge } from "@xyflow/react";

export function canonicalizeBuiltinWorkflowRef(
  ref: string,
  builtinWorkflowRefs: string[],
): string {
  if (!ref || ref.includes("://") || ref.includes("{{")) {
    return ref;
  }

  const canonicalBuiltinRef = `builtin://${ref}`;
  if (builtinWorkflowRefs.includes(canonicalBuiltinRef)) {
    return canonicalBuiltinRef;
  }

  return ref;
}

export function deriveWorkflowEntryFromEdges(
  explicitEntry: string | string[] | undefined,
  edges: Array<Pick<Edge, "source" | "target">>,
): string[] | undefined {
  const startTargets = Array.from(
    new Set(
      edges
        .filter((edge) => edge.source === "workflow")
        .map((edge) => edge.target)
        .filter((target) => target && target.length > 0),
    ),
  );

  if (startTargets.length > 0) {
    return startTargets;
  }

  if (explicitEntry === undefined) {
    return undefined;
  }

  return Array.isArray(explicitEntry) ? explicitEntry : [explicitEntry];
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function rewriteOrDropOutputExpression(
  expression: string,
  validNodeIds: Set<string>,
): string | undefined {
  const matches = Array.from(
    expression.matchAll(/\bnodes\.([A-Za-z][A-Za-z0-9_-]*)\b/g),
  );
  const referencedNodeIds = Array.from(new Set(matches.map((match) => match[1])));
  const staleNodeIds = referencedNodeIds.filter((nodeId) => !validNodeIds.has(nodeId));

  // Drop outputs that reference deleted nodes. Rewriting the node ID while keeping
  // the old field suffix (e.g. .message) is wrong when the replacement node has a
  // different type with different output fields.
  if (staleNodeIds.length > 0) {
    return undefined;
  }

  return expression;
}

export function sanitizeWorkflowReferences(
  entry: string | string[] | undefined,
  outputs: Record<string, string> | undefined,
  validNodeIds: Iterable<string>,
): {
  entry: string[] | undefined;
  outputs: Record<string, string> | undefined;
} {
  const validNodeIdSet = new Set(validNodeIds);
  const normalizedEntry = entry
    ? (Array.isArray(entry) ? entry : [entry]).filter((nodeId) =>
        validNodeIdSet.has(nodeId),
      )
    : [];
  const sanitizedEntry = normalizedEntry.length > 0 ? normalizedEntry : undefined;

  if (!outputs || Object.keys(outputs).length === 0) {
    return { entry: sanitizedEntry, outputs: undefined };
  }

  const sanitizedOutputs = Object.fromEntries(
    Object.entries(outputs)
      .map(([outputName, expression]) => [
        outputName,
        rewriteOrDropOutputExpression(expression, validNodeIdSet),
      ])
      .filter((entry): entry is [string, string] => typeof entry[1] === "string"),
  );

  return {
    entry: sanitizedEntry,
    outputs:
      Object.keys(sanitizedOutputs).length > 0 ? sanitizedOutputs : undefined,
  };
}