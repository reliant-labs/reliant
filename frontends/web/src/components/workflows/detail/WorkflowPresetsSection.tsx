// Copyright (c) 2025 Reliant Labs

/**
 * Workflow detail's Presets panel (WORKFLOW_UI.md §2.3, decision 7): the
 * presets that fit this workflow's preset groups, and "Default presets…",
 * which opens the PresetConfigModal the old hub's card offered. Every preset
 * across workflows is in Settings → Presets.
 */

import { useState } from "react";

import { useWorkflowPresets } from "@/hooks/preset-queries";
import Card from "../../forge-ui/card";
import { PresetConfigModal } from "../../workflow/presets/PresetConfigModal";
import { PresetList } from "../../workflow/presets/PresetList";

export function WorkflowPresetsSection({ projectId, workflowRef }: { projectId: string; workflowRef: string }) {
  // includeHidden: this is a management view, like the old hub's tab.
  const { presets, loading, error } = useWorkflowPresets(projectId, workflowRef, { includeHidden: true });
  const [configuring, setConfiguring] = useState(false);

  return (
    <section aria-label="Presets">
      <Card padding="none">
        <div className="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-3">
          <h2 className="text-sm font-semibold text-foreground">Presets</h2>
          <button
            type="button"
            onClick={() => setConfiguring(true)}
            className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Default presets…
          </button>
        </div>
        {error ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">Presets could not be loaded.</p>
        ) : loading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">Loading presets…</p>
        ) : presets.length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">
            No presets fit this workflow. Save one from the chat composer when you configure it.
          </p>
        ) : (
          <PresetList
            projectId={projectId}
            presets={presets}
            label="Presets for this workflow"
          />
        )}
      </Card>
      {configuring && (
        <PresetConfigModal
          workflowName={workflowRef}
          projectId={projectId}
          availablePresets={presets}
          onClose={() => setConfiguring(false)}
        />
      )}
    </section>
  );
}
