// Copyright (c) 2025 Reliant Labs

/**
 * A list of presets with the actions the old hub's Presets tab offered —
 * view, edit and delete your own, copy a built-in, hide from the picker — and
 * the three modals that carry them out (extracted unchanged into this
 * folder). Hosted by workflow detail's Presets section (one workflow's) and
 * Settings → Presets (all of them): WORKFLOW_UI.md §14.1 decision 7.
 */

import { useState } from "react";
import { toast } from "sonner";

import type { Preset } from "../../../store/globalDataStore";
import { presetGrpc } from "../../../api/preset-grpc";
import { usePreferencesStore } from "../../../store/preferencesStore";
import { RowMenu, type RowMenuAction } from "../../workflows/RowMenu";
import { WorkflowBadge, WORKFLOW_SOURCE_LABEL } from "../../workflows/WorkflowSourceBadge";
import { PresetEditModal } from "./PresetEditModal";
import { PresetViewModal } from "./PresetViewModal";

interface PresetListProps {
  projectId: string;
  presets: Preset[];
  /** Presets offered as parameter values in the edit form. */
  availablePresets?: Preset[];
  /** Refetch after a change. */
  onChanged: () => void;
  /** aria-label for the list. */
  label: string;
}

export function PresetList({ projectId, presets, availablePresets, onChanged, label }: PresetListProps) {
  const [viewing, setViewing] = useState<Preset | null>(null);
  const [editing, setEditing] = useState<Preset | null>(null);
  const isPresetHidden = usePreferencesStore((state) => state.isPresetHidden);
  const togglePresetVisibility = usePreferencesStore((state) => state.togglePresetVisibility);

  const remove = async (preset: Preset) => {
    if (!window.confirm(`Delete preset "${preset.name}"? This cannot be undone.`)) return;
    try {
      const result = await presetGrpc.deletePreset(projectId, preset.name);
      if (result.success) {
        toast.success(`Deleted "${preset.name}"`);
        onChanged();
      } else {
        toast.error(result.error || "Failed to delete preset");
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete preset");
    }
  };

  const toggleHidden = async (preset: Preset) => {
    try {
      const hidden = isPresetHidden(preset.name);
      await togglePresetVisibility(preset.name);
      toast.success(hidden ? `"${preset.name}" visible in preset picker` : `"${preset.name}" hidden from preset picker`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update visibility");
    }
  };

  const actionsFor = (preset: Preset): RowMenuAction[] => {
    const actions: RowMenuAction[] = [{ label: "View", onSelect: () => setViewing(preset) }];
    if (preset.source === "builtin") actions.push({ label: "Copy to new preset", onSelect: () => setViewing(preset) });
    if (preset.source === "user") actions.push({ label: "Edit", onSelect: () => setEditing(preset) });
    actions.push({
      label: isPresetHidden(preset.name) ? "Show in preset picker" : "Hide from preset picker",
      onSelect: () => void toggleHidden(preset),
    });
    if (preset.source === "user") actions.push({ label: "Delete", destructive: true, onSelect: () => void remove(preset) });
    return actions;
  };

  return (
    <>
      <ul aria-label={label} className="divide-y divide-border/60">
        {presets.map((preset) => {
          const name = typeof preset.name === "string" ? preset.name : JSON.stringify(preset.name);
          const tag = typeof preset.tag === "string" ? preset.tag : preset.tag ? JSON.stringify(preset.tag) : undefined;
          return (
            <li
              key={`${preset.source}-${name}`}
              className="flex items-center gap-3 px-4 py-2.5"
              data-testid={`preset-row-${name}`}
            >
              <button
                type="button"
                onClick={() => setViewing(preset)}
                className="min-w-0 flex-1 rounded-sm text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                <span className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="truncate text-sm font-medium text-foreground hover:underline">{name}</span>
                  <WorkflowBadge label={WORKFLOW_SOURCE_LABEL[preset.source as keyof typeof WORKFLOW_SOURCE_LABEL] ?? preset.source} variant="neutral" />
                  {tag && <span className="font-mono text-2xs text-muted-foreground">{tag}</span>}
                  {isPresetHidden(preset.name) && (
                    <span className="text-2xs font-medium uppercase text-muted-foreground">Hidden</span>
                  )}
                </span>
                {typeof preset.description === "string" && preset.description && (
                  <span className="mt-0.5 block truncate text-xs text-muted-foreground">{preset.description}</span>
                )}
              </button>
              <RowMenu label={`More actions for ${name}`} actions={actionsFor(preset)} />
            </li>
          );
        })}
      </ul>

      {viewing && (
        <PresetViewModal
          preset={viewing}
          projectId={projectId}
          onCopy={onChanged}
          onClose={() => setViewing(null)}
        />
      )}
      {editing && (
        <PresetEditModal
          preset={editing}
          projectId={projectId}
          availablePresets={availablePresets ?? presets}
          onSave={onChanged}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}
