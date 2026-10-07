// Copyright (c) 2025 Reliant Labs

/**
 * The canvas's keyboard path to building a graph, made visible.
 *
 * With one step selected it offers "Add step after <step>" (the same as
 * ⌘I); with two it offers "Connect <earlier> → <later>", with a swap, so an
 * edge can be drawn without dragging between 12px handles. Selection works
 * from the keyboard: Tab to a step, Enter selects it, ⌘/Ctrl+Enter adds a
 * second.
 */

import { Panel, useEdges, useNodes, type Node } from "@xyflow/react";
import { ArrowLeftRight, ArrowRight, Plus } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { getNodeDisplayName } from "../../../lib/node-metadata";
import { useBuilderShortcutLabel, useWorkflowBuilderShortcuts } from "../hooks/useWorkflowBuilderShortcuts";
import { useCanvasInsertApi } from "./CanvasInsertContext";
import { connectionOrder, connectionProblem, defaultSourceHandle, describeNode } from "./insertPlacement";

const buttonClass =
  "inline-flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

function Kbd({ children }: { children: string }) {
  if (!children) return null;
  return <kbd className="rounded border border-border/60 bg-background px-1 font-mono text-xs text-muted-foreground">{children}</kbd>;
}

/** A short name for the bar: the step's id, or "Start". */
function shortName(node: Node): string {
  return describeNode(node, getNodeDisplayName) === "the start" ? "Start" : node.id;
}

export function SelectionActions() {
  const api = useCanvasInsertApi();
  const nodes = useNodes();
  const edges = useEdges();
  const addShortcut = useBuilderShortcutLabel("openStepPalette");
  const connectShortcut = useBuilderShortcutLabel("connectSelectedSteps");

  const selected = nodes.filter((node) => node.selected);
  const selectionKey = selected.map((node) => node.id).sort().join(" ");
  // Which way round the pair runs, per selection: reset when it changes.
  const [swappedFor, setSwappedFor] = useState<string | null>(null);
  const swapped = swappedFor === selectionKey;

  const pair = selected.length === 2 ? connectionOrder(selected[0]!, selected[1]!) : null;
  const [source, target] = pair ? (swapped ? [pair[1], pair[0]] : pair) : [undefined, undefined];
  const problem = source && target ? connectionProblem(source, target, edges) : null;

  const connect = () => {
    if (!api || !source || !target) return;
    if (problem) {
      toast.error(problem);
      return;
    }
    if (api.connect(source.id, target.id)) toast.success(`Connected ${shortName(source)} → ${shortName(target)}`);
  };

  useWorkflowBuilderShortcuts({ onConnectSelectedSteps: api ? connect : undefined });

  if (!api || selected.length === 0 || selected.length > 2) return null;

  return (
    <Panel position="top-center" className="!mt-3">
      <div
        role="toolbar"
        aria-label="Selected steps"
        className="flex items-center gap-1 rounded-lg border border-border bg-card/95 p-1 shadow-md shadow-black/10 backdrop-blur-sm"
      >
        {selected.length === 1 ? (
          <button
            type="button"
            className={`${buttonClass} text-foreground hover:bg-muted`}
            onClick={() =>
              api.openPaletteAt({ kind: "after", nodeId: selected[0]!.id, sourceHandle: defaultSourceHandle(selected[0]!, edges) })
            }
          >
            <Plus className="h-3.5 w-3.5" aria-hidden />
            Add step after <span className="font-mono">{shortName(selected[0]!)}</span>
            <Kbd>{addShortcut}</Kbd>
          </button>
        ) : (
          source &&
          target && (
            <>
              <button
                type="button"
                className={`${buttonClass} text-foreground hover:bg-muted`}
                onClick={connect}
                disabled={!!problem}
                title={problem ?? undefined}
                aria-describedby={problem ? "selection-actions-problem" : undefined}
              >
                Connect <span className="font-mono">{shortName(source)}</span>
                <ArrowRight className="h-3.5 w-3.5" aria-label="to" />
                <span className="font-mono">{shortName(target)}</span>
                <Kbd>{connectShortcut}</Kbd>
              </button>
              <button
                type="button"
                className={`${buttonClass} text-muted-foreground hover:bg-muted hover:text-foreground`}
                onClick={() => setSwappedFor(swapped ? null : selectionKey)}
                aria-label="Swap direction"
                title="Swap direction"
              >
                <ArrowLeftRight className="h-3.5 w-3.5" aria-hidden />
              </button>
              {problem && (
                <span id="selection-actions-problem" className="px-1.5 text-xs text-muted-foreground">
                  {problem}
                </span>
              )}
            </>
          )
        )}
      </div>
    </Panel>
  );
}
