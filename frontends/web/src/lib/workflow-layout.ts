import type { Workflow, Step } from "../types/workflow";
import { getStepType, getStepWhile, getSwitchNodeId } from "../types/workflow";
import {
  NODE_DIMENSIONS,
  DEFAULT_DIMENSIONS,
  LAYOUT_CONSTANTS,
  estimateSwitchNodeHeight as sharedEstimateSwitchNodeHeight,
} from "./workflow-node-dimensions";

const { LAYER_SPACING, MIN_NODE_SPACING, START_X, START_Y } = LAYOUT_CONSTANTS;

// Structural node types that have their own dimensions
const STRUCTURAL_TYPES = new Set(["run", "workflow", "agent", "join", "loop", "router"]);

type ConditionValue = string | { expr?: string } | undefined;
type LegacyEdgeTarget = string | string[] | undefined;

function getConditionExpression(condition: ConditionValue): string {
  if (typeof condition === "string") {
    return condition;
  }
  return condition?.expr ?? "";
}

function toTargetArray(target: LegacyEdgeTarget): string[] {
  if (!target) {
    return [];
  }
  return Array.isArray(target) ? target : [target];
}

/**
 * Get estimated dimensions for a step
 */
function getNodeDimensions(step: Step): { width: number; height: number } {
  const stepType = getStepType(step);

  // For structural types, use their specific dimensions
  // For activity types (call_llm, save_message, etc.), use 'action' dimensions
  const lookupType = STRUCTURAL_TYPES.has(stepType) ? stepType : "action";
  const dims = NODE_DIMENSIONS[lookupType] || DEFAULT_DIMENSIONS;

  // For loop nodes, account for additional content
  if (stepType === "loop") {
    // Add height for while condition if present
    const hasWhile = getStepWhile(step) ? 30 : 0;
    return { width: dims.width, height: dims.height + hasWhile };
  }

  return { width: dims.width, height: dims.height };
}

/**
 * Estimate switch node height based on number of cases
 */
function getSwitchNodeHeight(numCases: number): number {
  return sharedEstimateSwitchNodeHeight(numCases);
}

/**
 * Build graph structure from workflow edges
 */
interface GraphNode {
  id: string;
  step?: Step;
  type: "step" | "switch" | "entry";
  layer: number;
  width: number;
  height: number;
  x: number;
  y: number;
  cases?: number; // For switch nodes
}

/**
 * Layout direction options
 */
export type LayoutDirection = "horizontal" | "vertical";

/**
 * Simple hierarchical layout algorithm for workflows
 * Positions nodes in layers based on their topological order
 * Now accounts for actual node heights to prevent overlap
 *
 * @param workflow - The workflow definition to layout
 * @param direction - Layout direction: 'horizontal' (left-to-right) or 'vertical' (top-to-bottom)
 */
export function autoLayoutWorkflow(
  workflow: Workflow,
  direction: LayoutDirection = "horizontal",
): Workflow {
  if (!workflow.nodes || workflow.nodes.length === 0) {
    return workflow;
  }

  // Filter out any nodes without a type field (invalid nodes)
  // This prevents crashes when workflow data is malformed
  const validNodes = workflow.nodes.filter((n) => {
    if (!n.type) {
      console.warn(
        "[autoLayoutWorkflow] Skipping node without type field:",
        n.id || "unknown",
      );
      return false;
    }
    return true;
  });

  if (validNodes.length === 0) {
    // Return workflow with empty nodes array instead of original invalid nodes
    return { ...workflow, nodes: [] };
  }

  // Always re-layout by clearing existing positions from ui.positions
  // This ensures clean layout when switching between horizontal and vertical directions
  // Note: Proto Step doesn't have position field - positions are in WorkflowUI.positions
  const workflowToLayout = {
    ...workflow,
    nodes: validNodes,
    ui: workflow.ui ? { ...workflow.ui, positions: {} } : undefined,
  };

  // Build graph info for all nodes
  const graphNodes = new Map<string, GraphNode>();

  // Add entry point
  graphNodes.set("workflow", {
    id: "workflow",
    type: "entry",
    layer: 0,
    width: NODE_DIMENSIONS.event.width,
    height: NODE_DIMENSIONS.event.height,
    x: 0,
    y: 0,
  });

  // Add workflow nodes
  workflowToLayout.nodes.forEach((step) => {
    if (!step.id) return; // Skip steps without id
    const dims = getNodeDimensions(step);
    graphNodes.set(step.id, {
      id: step.id,
      step,
      type: "step",
      layer: -1, // Will be assigned
      width: dims.width,
      height: dims.height,
      x: 0,
      y: 0,
    });
  });

  // Build adjacency map from edges
  const adjacency = new Map<string, Set<string>>();
  const inDegree = new Map<string, number>();

  // Track multi-case edges for switch node creation
  const switchEdges = new Map<
    string,
    { from: string; cases: Array<{ to: string; condition?: string }> }
  >();

  // Initialize all nodes with 0 in-degree
  graphNodes.forEach((_, id) => {
    inDegree.set(id, 0);
    adjacency.set(id, new Set());
  });

  // Process entry field (implicit edges from workflow start)
  if (workflowToLayout.entry) {
    const entryNodes = Array.isArray(workflowToLayout.entry) 
      ? workflowToLayout.entry 
      : [workflowToLayout.entry]
    
    entryNodes.forEach(targetId => {
      if (graphNodes.has(targetId)) {
        const sourceAdj = adjacency.get('workflow')
        if (sourceAdj) {
          sourceAdj.add(targetId)
          inDegree.set(targetId, (inDegree.get(targetId) || 0) + 1)
        }
      }
    })
  }

  // Process edges
  if (workflowToLayout.edges) {
    workflowToLayout.edges.forEach((edge) => {
      if (!edge.from) return; // Skip edges without from
      // Determine source node ID
      let sourceId: string;
      if (edge.from === "started" || edge.from === "workflow") {
        sourceId = "workflow";
      } else {
        sourceId = edge.from.split(".")[0];
      }

      const cases = edge.cases || [];
      const defaultTargets = toTargetArray(edge.default);
      const caseTargets = cases.flatMap((c) => toTargetArray(c.to));
      const allTargets = [...caseTargets, ...defaultTargets];

      // Check if this is a multi-target edge (creates a switch node)
      if (cases.length > 1 || (cases.length >= 1 && defaultTargets.length > 0)) {
        const switchId = getSwitchNodeId(edge.from || sourceId);
        switchEdges.set(switchId, {
          from: sourceId,
          cases: cases.flatMap((c) =>
            toTargetArray(c.to).map((to) => ({
              to,
              condition: getConditionExpression(c.condition) || undefined,
            })),
          ),
        });

        // Create switch node
        const switchHeight = getSwitchNodeHeight(cases.length + (defaultTargets.length > 0 ? 1 : 0));
        graphNodes.set(switchId, {
          id: switchId,
          type: "switch",
          layer: -1,
          width: NODE_DIMENSIONS.switch.width,
          height: switchHeight,
          x: 0,
          y: 0,
          cases: cases.length + (defaultTargets.length > 0 ? 1 : 0),
        });
        inDegree.set(switchId, 0);
        adjacency.set(switchId, new Set());

        // Source -> Switch
        const sourceAdj = adjacency.get(sourceId);
        if (sourceAdj) {
          sourceAdj.add(switchId);
        }
        inDegree.set(switchId, (inDegree.get(switchId) || 0) + 1);

        // Switch -> each target (cases + default)
        const switchAdj = adjacency.get(switchId)!;
        for (const targetId of allTargets) {
          if (graphNodes.has(targetId)) {
            switchAdj.add(targetId);
            inDegree.set(targetId, (inDegree.get(targetId) || 0) + 1);
          }
        }
      } else {
        // Single target - direct edge (either one case or one default)
        for (const targetId of allTargets) {
          const sourceAdj = adjacency.get(sourceId);
          if (sourceAdj && graphNodes.has(targetId)) {
            sourceAdj.add(targetId);
            inDegree.set(targetId, (inDegree.get(targetId) || 0) + 1);
          }
        }
      }
    });
  }

  // Topological sort to determine layers using BFS
  const queue: string[] = [];
  const visited = new Set<string>();

  // Start with entry point
  queue.push("workflow");
  const workflowNode = graphNodes.get("workflow")!;
  workflowNode.layer = 0;

  // Process nodes to assign layers
  while (queue.length > 0) {
    const current = queue.shift()!;

    if (visited.has(current)) continue;
    visited.add(current);

    const node = graphNodes.get(current)!;
    const neighbors = adjacency.get(current);

    if (neighbors) {
      neighbors.forEach((neighbor) => {
        const neighborNode = graphNodes.get(neighbor)!;
        const newLayer = node.layer + 1;

        // Update layer if it's a longer path
        if (neighborNode.layer < newLayer) {
          neighborNode.layer = newLayer;
        }

        if (!visited.has(neighbor)) {
          queue.push(neighbor);
        }
      });
    }
  }

  // Handle disconnected nodes
  graphNodes.forEach((node) => {
    if (node.layer < 0) {
      // Disconnected node - place in first layer
      node.layer = 1;
    }
  });

  // Group nodes by layer
  const layers: string[][] = [];
  graphNodes.forEach((node) => {
    if (!layers[node.layer]) {
      layers[node.layer] = [];
    }
    layers[node.layer].push(node.id);
  });

  // Build a reverse adjacency map for quick parent lookups
  const reverseAdjacency = new Map<string, string[]>();
  graphNodes.forEach((_, id) => reverseAdjacency.set(id, []));
  adjacency.forEach((targets, sourceId) => {
    targets.forEach((targetId) => {
      const parents = reverseAdjacency.get(targetId);
      if (parents) parents.push(sourceId);
    });
  });

  // Calculate positions for each layer
  layers.forEach((layerNodes, layerIndex) => {
    if (direction === "horizontal") {
      // Horizontal layout: layers go left-to-right
      const x = START_X + layerIndex * LAYER_SPACING;

      // Categorize each node: straight-line aligned, fan-out target, join (multi-source), or orphan
      const processed = new Set<string>();

      // --- Pass 1: Straight-line alignment ---
      // A node gets straight-line alignment when:
      //   - It has exactly one incoming source, AND
      //   - That source has exactly one outgoing target (1:1 chain)
      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceNode = graphNodes.get(sourceId);
          const sourceOutgoing = adjacency.get(sourceId);

          if (sourceNode && sourceOutgoing && sourceOutgoing.size === 1) {
            // 1:1 chain — align center-to-center vertically
            const sourceCenterY = sourceNode.y + sourceNode.height / 2;
            node.x = x;
            node.y = sourceCenterY - node.height / 2;
            processed.add(nodeId);
          }
        }
      });

      // --- Pass 2: Fan-out targets (switch / multi-outgoing) ---
      // Group unprocessed nodes by their source, then fan them vertically around source center
      const fanGroups = new Map<string, string[]>(); // sourceId → [targetIds in this layer]
      layerNodes.forEach((nodeId) => {
        if (processed.has(nodeId)) return;
        const incomingSources = reverseAdjacency.get(nodeId) || [];
        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceOutgoing = adjacency.get(sourceId);
          if (sourceOutgoing && sourceOutgoing.size > 1) {
            // This is a fan-out target
            if (!fanGroups.has(sourceId)) fanGroups.set(sourceId, []);
            fanGroups.get(sourceId)!.push(nodeId);
          }
        }
      });

      fanGroups.forEach((targetIds, sourceId) => {
        const sourceNode = graphNodes.get(sourceId);
        if (!sourceNode) return;
        const sourceCenterY = sourceNode.y + sourceNode.height / 2;

        // Calculate total height of the fan block
        let totalFanHeight = 0;
        targetIds.forEach((id) => {
          totalFanHeight += graphNodes.get(id)!.height;
        });
        totalFanHeight += (targetIds.length - 1) * MIN_NODE_SPACING;

        // Center the fan block on the source center
        let currentY = sourceCenterY - totalFanHeight / 2;
        targetIds.forEach((id) => {
          const node = graphNodes.get(id)!;
          node.x = x;
          node.y = currentY;
          currentY += node.height + MIN_NODE_SPACING;
          processed.add(id);
        });
      });

      // --- Pass 3: Join nodes (multiple incoming sources) ---
      // Center on the vertical midpoint of all sources
      layerNodes.forEach((nodeId) => {
        if (processed.has(nodeId)) return;
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length > 1) {
          const sourceYs = incomingSources
            .map((id) => graphNodes.get(id))
            .filter((n): n is GraphNode => n !== undefined)
            .map((n) => n.y + n.height / 2);

          if (sourceYs.length > 0) {
            const centerY =
              sourceYs.reduce((a, b) => a + b, 0) / sourceYs.length;
            node.x = x;
            node.y = centerY - node.height / 2;
            processed.add(nodeId);
          }
        }
      });

      // --- Pass 4: Remaining orphan / unconnected nodes ---
      const remainingNodes = layerNodes.filter((id) => !processed.has(id));
      if (remainingNodes.length > 0) {
        // Compute a reasonable center Y from already-placed siblings in this layer
        const placedYs = layerNodes
          .filter((id) => processed.has(id))
          .map((id) => {
            const n = graphNodes.get(id)!;
            return n.y + n.height / 2;
          });
        const centerY =
          placedYs.length > 0
            ? placedYs.reduce((a, b) => a + b, 0) / placedYs.length
            : START_Y;

        let totalHeight = 0;
        remainingNodes.forEach((id) => {
          totalHeight += graphNodes.get(id)!.height;
        });
        totalHeight += (remainingNodes.length - 1) * MIN_NODE_SPACING;

        let currentY = centerY - totalHeight / 2;
        remainingNodes.forEach((id) => {
          const node = graphNodes.get(id)!;
          node.x = x;
          node.y = currentY;
          currentY += node.height + MIN_NODE_SPACING;
          processed.add(id);
        });
      }

      // Ensure all nodes in this layer have their x set
      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        if (node.x === 0 && layerIndex > 0) node.x = x;
      });
    } else {
      // Vertical layout: layers go top-to-bottom
      const y = START_Y + layerIndex * LAYER_SPACING;

      const processed = new Set<string>();

      // --- Pass 1: Straight-line alignment (1:1 chains) ---
      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceNode = graphNodes.get(sourceId);
          const sourceOutgoing = adjacency.get(sourceId);

          if (sourceNode && sourceOutgoing && sourceOutgoing.size === 1) {
            const sourceCenterX = sourceNode.x + sourceNode.width / 2;
            node.x = sourceCenterX - node.width / 2;
            node.y = y;
            processed.add(nodeId);
          }
        }
      });

      // --- Pass 2: Fan-out targets ---
      const fanGroups = new Map<string, string[]>();
      layerNodes.forEach((nodeId) => {
        if (processed.has(nodeId)) return;
        const incomingSources = reverseAdjacency.get(nodeId) || [];
        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceOutgoing = adjacency.get(sourceId);
          if (sourceOutgoing && sourceOutgoing.size > 1) {
            if (!fanGroups.has(sourceId)) fanGroups.set(sourceId, []);
            fanGroups.get(sourceId)!.push(nodeId);
          }
        }
      });

      fanGroups.forEach((targetIds, sourceId) => {
        const sourceNode = graphNodes.get(sourceId);
        if (!sourceNode) return;
        const sourceCenterX = sourceNode.x + sourceNode.width / 2;

        let totalFanWidth = 0;
        targetIds.forEach((id) => {
          totalFanWidth += graphNodes.get(id)!.width;
        });
        totalFanWidth += (targetIds.length - 1) * MIN_NODE_SPACING;

        let currentX = sourceCenterX - totalFanWidth / 2;
        targetIds.forEach((id) => {
          const node = graphNodes.get(id)!;
          node.x = currentX;
          node.y = y;
          currentX += node.width + MIN_NODE_SPACING;
          processed.add(id);
        });
      });

      // --- Pass 3: Join nodes ---
      layerNodes.forEach((nodeId) => {
        if (processed.has(nodeId)) return;
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length > 1) {
          const sourceXs = incomingSources
            .map((id) => graphNodes.get(id))
            .filter((n): n is GraphNode => n !== undefined)
            .map((n) => n.x + n.width / 2);

          if (sourceXs.length > 0) {
            const centerX = sourceXs.reduce((a, b) => a + b, 0) / sourceXs.length;
            node.x = centerX - node.width / 2;
            node.y = y;
            processed.add(nodeId);
          }
        }
      });

      // --- Pass 4: Remaining orphan nodes ---
      const remainingNodes = layerNodes.filter((id) => !processed.has(id));
      if (remainingNodes.length > 0) {
        const placedXs = layerNodes
          .filter((id) => processed.has(id))
          .map((id) => {
            const n = graphNodes.get(id)!;
            return n.x + n.width / 2;
          });
        const centerX =
          placedXs.length > 0
            ? placedXs.reduce((a, b) => a + b, 0) / placedXs.length
            : START_X;

        let totalWidth = 0;
        remainingNodes.forEach((id) => {
          totalWidth += graphNodes.get(id)!.width;
        });
        totalWidth += (remainingNodes.length - 1) * MIN_NODE_SPACING;

        let currentX = centerX - totalWidth / 2;
        remainingNodes.forEach((id) => {
          const node = graphNodes.get(id)!;
          node.x = currentX;
          node.y = y;
          currentX += node.width + MIN_NODE_SPACING;
          processed.add(id);
        });
      }

      // Ensure all nodes in this layer have their y set
      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        if (node.y === 0 && layerIndex > 0) node.y = y;
      });
    }
  });

  // Now we need to handle a specific case: switch nodes should be positioned
  // between their source and targets, not just in sequence
  switchEdges.forEach((switchData, switchId) => {
    const switchNode = graphNodes.get(switchId);
    if (!switchNode) return;

    const sourceNode = graphNodes.get(switchData.from);
    if (!sourceNode) return;

    // Find target nodes
    const targetNodes = switchData.cases
      .map((c) => graphNodes.get(c.to))
      .filter((n): n is GraphNode => n !== undefined);

    if (targetNodes.length > 0) {
      if (direction === "horizontal") {
        // For horizontal layout, position switch to evenly split to targets
        // Calculate the center Y of all targets
        const targetMinY = Math.min(...targetNodes.map((n) => n.y));
        const targetMaxY = Math.max(...targetNodes.map((n) => n.y + n.height));
        const targetCenterY = (targetMinY + targetMaxY) / 2;

        // Position switch at the center of targets, aligned with source if possible
        // This creates an even split
        switchNode.y = targetCenterY - switchNode.height / 2;

        // Ensure switch doesn't overlap with source
        if (
          switchNode.y <
          sourceNode.y + sourceNode.height + MIN_NODE_SPACING
        ) {
          switchNode.y = sourceNode.y + sourceNode.height + MIN_NODE_SPACING;
        }
      } else {
        // Vertical layout: position switch to evenly split to targets horizontally
        // Calculate the center X of all targets
        const targetMinX = Math.min(...targetNodes.map((n) => n.x));
        const targetMaxX = Math.max(...targetNodes.map((n) => n.x + n.width));
        const targetCenterX = (targetMinX + targetMaxX) / 2;

        // Position switch at the center of targets, aligned with source if possible
        // This creates an even split
        switchNode.x = targetCenterX - switchNode.width / 2;

        // Ensure switch doesn't overlap with source
        if (switchNode.x < sourceNode.x + sourceNode.width + MIN_NODE_SPACING) {
          switchNode.x = sourceNode.x + sourceNode.width + MIN_NODE_SPACING;
        }
      }
    }
  });

  // Run collision detection and resolution
  resolveCollisions(graphNodes, layers, direction);

  // Final pass: refine straight-line alignments where 1:1 chains got displaced by collision resolution
  if (direction === "horizontal") {
    layers.forEach((layerNodes, layerIndex) => {
      if (layerIndex === 0) return;

      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceNode = graphNodes.get(sourceId);
          const sourceOutgoing = adjacency.get(sourceId);

          // Only re-align 1:1 chains (source has single outgoing)
          if (sourceNode && sourceOutgoing && sourceOutgoing.size === 1) {
            const sourceCenterY = sourceNode.y + sourceNode.height / 2;
            const alignedY = sourceCenterY - node.height / 2;

            // Check if aligned position conflicts with other nodes in same layer
            let canAlign = true;
            layerNodes.forEach((otherId) => {
              if (otherId === nodeId) return;
              const otherNode = graphNodes.get(otherId)!;
              if (
                Math.abs(otherNode.y - alignedY) <
                (node.height + otherNode.height) / 2 + MIN_NODE_SPACING
              ) {
                canAlign = false;
              }
            });

            if (canAlign) {
              node.y = alignedY;
            }
          }
        }
      });
    });

    resolveCollisions(graphNodes, layers, direction);
  } else if (direction === "vertical") {
    layers.forEach((layerNodes, layerIndex) => {
      if (layerIndex === 0) return;

      layerNodes.forEach((nodeId) => {
        const node = graphNodes.get(nodeId)!;
        const incomingSources = reverseAdjacency.get(nodeId) || [];

        if (incomingSources.length === 1) {
          const sourceId = incomingSources[0];
          const sourceNode = graphNodes.get(sourceId);
          const sourceOutgoing = adjacency.get(sourceId);

          if (sourceNode && sourceOutgoing && sourceOutgoing.size === 1) {
            const sourceCenterX = sourceNode.x + sourceNode.width / 2;
            const alignedX = sourceCenterX - node.width / 2;

            let canAlign = true;
            layerNodes.forEach((otherId) => {
              if (otherId === nodeId) return;
              const otherNode = graphNodes.get(otherId)!;
              if (
                Math.abs(otherNode.x - alignedX) <
                (node.width + otherNode.width) / 2 + MIN_NODE_SPACING
              ) {
                canAlign = false;
              }
            });

            if (canAlign) {
              node.x = alignedX;
            }
          }
        }
      });
    });

    resolveCollisions(graphNodes, layers, direction);
  }

  // Collect all positions (nodes, switches, workflow entry) into ui.positions
  // Proto Step doesn't have a position field - all positions go in WorkflowUI.positions
  const allPositions: Record<string, { x: number; y: number }> = {
    ...(workflowToLayout.ui?.positions ? 
      Object.fromEntries(
        Object.entries(workflowToLayout.ui.positions).map(([k, v]) => {
          const pos = v as { x?: number; y?: number };
          return [k, { x: pos.x ?? 0, y: pos.y ?? 0 }];
        })
      ) : {}),
  };

  // Add node positions
  workflowToLayout.nodes.forEach((step) => {
    if (!step.id) return;
    const node = graphNodes.get(step.id);
    if (node) {
      allPositions[step.id] = { x: node.x, y: node.y };
    }
  });

  // Add switch positions
  graphNodes.forEach((node, id) => {
    if (node.type === "switch") {
      allPositions[id] = { x: node.x, y: node.y };
    }
  });

  // Add workflow entry point position
  const workflowEntryNode = graphNodes.get("workflow");
  allPositions["workflow"] = workflowEntryNode
    ? { x: workflowEntryNode.x, y: workflowEntryNode.y }
    : { x: START_X, y: START_Y };

  // Return workflow with positions in ui.positions
  // Use type assertion for positions since proto Position extends Message
  return {
    ...workflowToLayout,
    nodes: workflowToLayout.nodes, // Steps don't have position field in proto
    ui: {
      ...workflowToLayout.ui,
      positions: allPositions,
    },
  } as Workflow;
}

/**
 * Resolve collisions between nodes in the same layer
 */
function resolveCollisions(
  graphNodes: Map<string, GraphNode>,
  layers: string[][],
  direction: LayoutDirection = "horizontal",
): void {
  // Process each layer
  layers.forEach((layerNodes) => {
    if (layerNodes.length <= 1) return;

    if (direction === "horizontal") {
      // Sort nodes by Y position
      const nodes = layerNodes
        .map((id) => graphNodes.get(id)!)
        .filter((n) => n)
        .sort((a, b) => a.y - b.y);

      // Check for overlaps and resolve
      let maxIterations = 10;
      let hasOverlap = true;

      while (hasOverlap && maxIterations > 0) {
        hasOverlap = false;

        for (let i = 0; i < nodes.length - 1; i++) {
          const current = nodes[i];
          const next = nodes[i + 1];

          // Check if nodes overlap (with minimum spacing)
          const currentBottom = current.y + current.height + MIN_NODE_SPACING;
          if (currentBottom > next.y) {
            // Overlap detected - push the next node down
            const overlap = currentBottom - next.y;
            next.y += overlap;
            hasOverlap = true;
          }
        }

        maxIterations--;
      }
    } else {
      // Vertical layout: sort nodes by X position
      const nodes = layerNodes
        .map((id) => graphNodes.get(id)!)
        .filter((n) => n)
        .sort((a, b) => a.x - b.x);

      // Check for overlaps and resolve
      let maxIterations = 10;
      let hasOverlap = true;

      while (hasOverlap && maxIterations > 0) {
        hasOverlap = false;

        for (let i = 0; i < nodes.length - 1; i++) {
          const current = nodes[i];
          const next = nodes[i + 1];

          // Check if nodes overlap (with minimum spacing)
          const currentRight = current.x + current.width + MIN_NODE_SPACING;
          if (currentRight > next.x) {
            // Overlap detected - push the next node right
            const overlap = currentRight - next.x;
            next.x += overlap;
            hasOverlap = true;
          }
        }

        maxIterations--;
      }
    }
  });

  // Cross-layer collision detection
  // Check if any nodes from adjacent layers overlap
  for (let l = 0; l < layers.length - 1; l++) {
    const currentLayer = layers[l];
    const nextLayer = layers[l + 1];

    if (!currentLayer || !nextLayer) continue;

    const currentNodes = currentLayer
      .map((id) => graphNodes.get(id)!)
      .filter((n) => n);
    const nextNodes = nextLayer
      .map((id) => graphNodes.get(id)!)
      .filter((n) => n);

    if (direction === "horizontal") {
      // For each node in the current layer, check if any node in the next layer
      // horizontally overlaps and needs vertical adjustment
      for (const current of currentNodes) {
        for (const next of nextNodes) {
          // Check horizontal overlap
          const horizontalOverlap = !(
            current.x + current.width + 20 < next.x ||
            next.x + next.width + 20 < current.x
          );

          if (horizontalOverlap) {
            // Check vertical overlap
            const verticalOverlap = !(
              current.y + current.height + MIN_NODE_SPACING < next.y ||
              next.y + next.height + MIN_NODE_SPACING < current.y
            );

            if (verticalOverlap) {
              // Nodes overlap - push the next node down
              next.y = current.y + current.height + MIN_NODE_SPACING;
            }
          }
        }
      }
    } else {
      // Vertical layout: check if nodes vertically overlap and need horizontal adjustment
      for (const current of currentNodes) {
        for (const next of nextNodes) {
          // Check vertical overlap
          const verticalOverlap = !(
            current.y + current.height + 20 < next.y ||
            next.y + next.height + 20 < current.y
          );

          if (verticalOverlap) {
            // Check horizontal overlap
            const horizontalOverlap = !(
              current.x + current.width + MIN_NODE_SPACING < next.x ||
              next.x + next.width + MIN_NODE_SPACING < current.x
            );

            if (horizontalOverlap) {
              // Nodes overlap - push the next node right
              next.x = current.x + current.width + MIN_NODE_SPACING;
            }
          }
        }
      }
    }
  }
}