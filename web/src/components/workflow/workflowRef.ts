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
  fallbackNodeId?: string,
): string | undefined {
  const matches = Array.from(
    expression.matchAll(/\bnodes\.([A-Za-z][A-Za-z0-9_-]*)\b/g),
  );
  const referencedNodeIds = Array.from(new Set(matches.map((match) => match[1])));
  const staleNodeIds = referencedNodeIds.filter((nodeId) => !validNodeIds.has(nodeId));

  if (staleNodeIds.length > 0 && !fallbackNodeId) {
    return undefined;
  }

  let rewrittenExpression = expression;
  for (const staleNodeId of staleNodeIds) {
    const dottedNodePattern = new RegExp(
      `\\bnodes\\.${escapeRegExp(staleNodeId)}\\b`,
      "g",
    );
    rewrittenExpression = rewrittenExpression.replace(
      dottedNodePattern,
      `nodes.${fallbackNodeId}`,
    );
  }

  return rewrittenExpression;
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
  const fallbackNodeId = sanitizedEntry?.length === 1 ? sanitizedEntry[0] : undefined;

  if (!outputs || Object.keys(outputs).length === 0) {
    return { entry: sanitizedEntry, outputs: undefined };
  }

  const sanitizedOutputs = Object.fromEntries(
    Object.entries(outputs)
      .map(([outputName, expression]) => [
        outputName,
        rewriteOrDropOutputExpression(expression, validNodeIdSet, fallbackNodeId),
      ])
      .filter((entry): entry is [string, string] => typeof entry[1] === "string"),
  );

  return {
    entry: sanitizedEntry,
    outputs:
      Object.keys(sanitizedOutputs).length > 0 ? sanitizedOutputs : undefined,
  };
}