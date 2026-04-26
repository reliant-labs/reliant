// Copyright (c) 2025 Reliant Labs

import { ChevronLeft, X, Check, Layers } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { Preset } from "../../../store/globalDataStore";

interface PresetSettingsPageProps {
  presets: Preset[];
  selectedPreset: string | null;
  onPresetChange: (preset: Preset | null) => void;
  onBack: () => void;
  onClose: () => void;
}

export function PresetSettingsPage({
  presets,
  selectedPreset,
  onPresetChange,
  onBack,
  onClose,
}: PresetSettingsPageProps) {
  const userPresets = presets.filter((p) => p.source === "user");
  const projectPresets = presets.filter((p) => p.source === "project");
  const builtinPresets = presets.filter((p) => p.source === "builtin");

  const handleSelect = (preset: Preset | null) => {
    onPresetChange(preset);
  };

  return (
    <div>
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2.5 border-b border-border">
        <button onClick={onBack} className="p-1 rounded hover:bg-accent transition-colors">
          <ChevronLeft className="w-4 h-4 text-muted-foreground" />
        </button>
        <h3 className="text-sm font-semibold flex-1">Preset</h3>
        <button onClick={onClose} className="p-1 rounded hover:bg-accent transition-colors">
          <X className="w-3.5 h-3.5 text-muted-foreground" />
        </button>
      </div>

      {/* Scrollable content */}
      <div className="overflow-y-auto max-h-[350px] py-1">
        {/* None option */}
        <button
          onClick={() => handleSelect(null)}
          className={cn(
            "w-full px-4 py-2.5 text-left text-[13px] transition-colors flex items-center justify-between",
            !selectedPreset ? "bg-accent" : "hover:bg-accent/50"
          )}
        >
          <span className="text-muted-foreground">None (use defaults)</span>
          {!selectedPreset && <Check className="w-3.5 h-3.5 text-primary flex-shrink-0" />}
        </button>

        {/* User Presets */}
        {userPresets.length > 0 && (
          <PresetGroup
            label="User"
            presets={userPresets}
            selectedPreset={selectedPreset}
            onSelect={handleSelect}
          />
        )}

        {/* Project Presets */}
        {projectPresets.length > 0 && (
          <PresetGroup
            label="Project"
            presets={projectPresets}
            selectedPreset={selectedPreset}
            onSelect={handleSelect}
          />
        )}

        {/* Built-in Presets */}
        {builtinPresets.length > 0 && (
          <PresetGroup
            label="Built-in"
            presets={builtinPresets}
            selectedPreset={selectedPreset}
            onSelect={handleSelect}
          />
        )}

        {presets.length === 0 && (
          <div className="px-4 py-6 text-center text-sm text-muted-foreground">
            No presets available
          </div>
        )}
      </div>
    </div>
  );
}

function PresetGroup({
  label,
  presets,
  selectedPreset,
  onSelect,
}: {
  label: string;
  presets: Preset[];
  selectedPreset: string | null;
  onSelect: (preset: Preset) => void;
}) {
  return (
    <>
      <div className="px-4 py-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
        {label}
      </div>
      {presets.map((preset) => (
        <button
          key={preset.name}
          onClick={() => onSelect(preset)}
          className={cn(
            "w-full px-4 py-2.5 text-left text-[13px] transition-colors",
            selectedPreset === preset.name ? "bg-accent" : "hover:bg-accent/50"
          )}
        >
          <div className="flex items-start justify-between gap-2">
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-1.5">
                <Layers className="w-3 h-3 opacity-50 flex-shrink-0" />
                <span className="font-medium truncate">{preset.name}</span>
              </div>
              {preset.description && (
                <div className="text-[11px] text-muted-foreground mt-0.5 ml-[18px] line-clamp-1">
                  {preset.description}
                </div>
              )}
            </div>
            {selectedPreset === preset.name && (
              <Check className="w-3.5 h-3.5 text-primary flex-shrink-0 mt-0.5" />
            )}
          </div>
        </button>
      ))}
    </>
  );
}

export default PresetSettingsPage;
