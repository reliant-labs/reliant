// Copyright (c) 2025 Reliant Labs

/**
 * Settings → Presets: every preset in the current project, across workflows
 * (WORKFLOW_UI.md §14.1 decision 7). It replaces the old workflow hub's
 * Presets tab; one workflow's presets are also on its detail page in the
 * Workflows Library.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { presetGrpc, type InvalidPreset } from "@/api/preset-grpc";
import type { Preset } from "@/store/globalDataStore";
import { useProjectStore } from "@/store/projectStore";
import { cn } from "@/lib/utils";
import Card from "../forge-ui/card";
import { PresetList } from "../workflow/presets/PresetList";

export function PresetsSettings() {
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const [presets, setPresets] = useState<Preset[] | null>(null);
  const [invalid, setInvalid] = useState<InvalidPreset[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!projectId) return;
    try {
      const result = await presetGrpc.listPresetsWithErrors(projectId, true);
      setPresets(result.presets);
      setInvalid(result.invalidPresets);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const { mine, builtin } = useMemo(() => {
    const all = presets ?? [];
    const byName = (a: Preset, b: Preset) => a.name.localeCompare(b.name);
    return {
      mine: all.filter((p) => p.source !== "builtin").sort(byName),
      builtin: all.filter((p) => p.source === "builtin").sort(byName),
    };
  }, [presets]);

  return (
    <div className="space-y-6 px-8 py-8">
      <div>
        <h2 className="text-lg font-semibold text-foreground">Presets</h2>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          Saved settings for a workflow's inputs. Create one from the chat composer when you configure a
          workflow; a workflow's own presets and its defaults are also on its page in Workflows.
        </p>
      </div>

      {!projectId ? (
        <Card padding="lg">
          <p className="text-sm text-muted-foreground">Presets belong to a project. Open a project to see its presets.</p>
        </Card>
      ) : error ? (
        <Card padding="lg" role="alert">
          <p className="text-sm font-medium text-foreground">Presets could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{error}</p>
        </Card>
      ) : presets === null ? (
        <Card padding="lg" aria-busy="true">
          <p className="text-sm text-muted-foreground">Loading presets…</p>
        </Card>
      ) : (
        <>
          <PresetGroup label="Your presets" count={mine.length}>
            {mine.length === 0 ? (
              <p className="px-4 py-3 text-sm text-muted-foreground">No presets of your own yet.</p>
            ) : (
              <PresetList projectId={projectId} presets={mine} availablePresets={presets} onChanged={() => void load()} label="Your presets" />
            )}
          </PresetGroup>
          {builtin.length > 0 && (
            <PresetGroup label="Built-in" count={builtin.length}>
              <PresetList projectId={projectId} presets={builtin} availablePresets={presets} onChanged={() => void load()} label="Built-in presets" />
            </PresetGroup>
          )}
          {invalid.length > 0 && (
            <PresetGroup label="Failed to load" count={invalid.length} danger>
              <ul aria-label="Presets that failed to load" className="divide-y divide-border/60">
                {invalid.map((preset) => (
                  <li key={`${preset.source}-${preset.name}`} className="px-4 py-2.5">
                    <p className="text-sm font-medium text-foreground">{preset.name}</p>
                    <p className="truncate font-mono text-xs text-muted-foreground" title={preset.path}>{preset.path}</p>
                    {preset.errors.map((err, index) => (
                      <p key={index} className="text-xs text-destructive">{err}</p>
                    ))}
                  </li>
                ))}
              </ul>
            </PresetGroup>
          )}
        </>
      )}
    </div>
  );
}

function PresetGroup({
  label,
  count,
  danger = false,
  children,
}: {
  label: string;
  count: number;
  danger?: boolean;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={label}>
      <h3
        className={cn(
          "mb-2 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide",
          danger ? "text-destructive" : "text-muted-foreground",
        )}
      >
        {danger && <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />}
        {label}
        <span className="font-medium opacity-70">{count}</span>
      </h3>
      <Card padding="none" className={cn(danger && "border-destructive/40")}>{children}</Card>
    </section>
  );
}
