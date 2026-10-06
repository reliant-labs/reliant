// Copyright (c) 2025 Reliant Labs

/**
 * A "+" beside every node output that leads nowhere yet: the start node
 * before the first step, the last step of a path, an unused Switch case.
 * It opens the step palette, and the step chosen there lands after that
 * output, connected (n8n's plus-handle, `CanvasHandlePlus.vue`).
 *
 * An output that is already connected shows no "+": inserting there is the
 * "+" on its edge, and a parallel branch is "Add step" with the node selected.
 *
 * Rendered once for the whole canvas, in flow coordinates through
 * ViewportPortal and positioned from React Flow's measured handle bounds, so
 * no node component knows about it and every node type — including ones
 * added later — gets it.
 */

import { Position, ViewportPortal, useEdges, useInternalNode, useNodes, type Edge, type Node } from "@xyflow/react";
import { Plus } from "lucide-react";
import type { MouseEvent } from "react";

import { getNodeDisplayName } from "../../../lib/node-metadata";
import { cn } from "../../../lib/utils";
import { useCanvasInsertApi, type CanvasInsertApi } from "./CanvasInsertContext";
import { describeCase, describeNode } from "./insertPlacement";

/** The button's diameter and its distance from the handle, in flow pixels. */
const BUTTON_SIZE = 22;
const BUTTON_GAP = 12;

export function NodeOutputAddButtons() {
  const api = useCanvasInsertApi();
  const nodes = useNodes();
  const edges = useEdges();
  if (!api) return null;
  return (
    <ViewportPortal>
      {nodes
        .filter((node) => !node.hidden && !node.parentId)
        .map((node) => (
          <NodeOutputAdd key={node.id} node={node} edges={edges} api={api} />
        ))}
    </ViewportPortal>
  );
}

function NodeOutputAdd({ node, edges, api }: { node: Node; edges: Edge[]; api: CanvasInsertApi }) {
  const internal = useInternalNode(node.id);
  const origin = internal?.internals.positionAbsolute;
  const outputs = internal?.internals.handleBounds?.source ?? [];
  if (!origin || outputs.length === 0) return null;

  const name = describeNode(node, getNodeDisplayName);
  return (
    <>
      {outputs
        .filter((handle) => !edges.some((edge) => edge.source === node.id && (edge.sourceHandle ?? null) === (handle.id ?? null)))
        .map((handle) => {
          const below = handle.position === Position.Bottom;
          const left = below ? origin.x + handle.x + handle.width / 2 - BUTTON_SIZE / 2 : origin.x + handle.x + handle.width + BUTTON_GAP;
          const top = below ? origin.y + handle.y + handle.height + BUTTON_GAP : origin.y + handle.y + handle.height / 2 - BUTTON_SIZE / 2;
          const caseName = describeCase(node, handle.id);
          const label = caseName ? `Add a step after ${name}, case ${caseName}` : `Add a step after ${name}`;
          const open = (event: MouseEvent) => {
            event.stopPropagation();
            api.openPaletteAt({ kind: "after", nodeId: node.id, sourceHandle: handle.id ?? null });
          };
          return (
            <button
              key={handle.id ?? "output"}
              type="button"
              aria-label={label}
              title={label}
              data-testid={`node-output-add-${node.id}${handle.id ? `-${handle.id}` : ""}`}
              onClick={open}
              // The pane would otherwise start a pan or a selection box.
              onMouseDown={(event) => event.stopPropagation()}
              onPointerDown={(event) => event.stopPropagation()}
              className={cn(
                "nodrag nopan pointer-events-auto absolute left-0 top-0 flex items-center justify-center rounded-full",
                "border border-border bg-card text-muted-foreground shadow-sm transition-colors",
                "hover:border-primary hover:bg-primary hover:text-primary-foreground",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              )}
              style={{ width: BUTTON_SIZE, height: BUTTON_SIZE, transform: `translate(${left}px, ${top}px)` }}
            >
              <Plus className="h-3.5 w-3.5" aria-hidden />
            </button>
          );
        })}
    </>
  );
}
