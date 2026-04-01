// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect } from "react";
import { ChevronDown, Check, Layers } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import type { Preset } from "../../store/globalDataStore";

interface InlinePresetPickerProps {
  // Available presets (filtered for the current workflow)
  presets: Preset[];
  
  // Currently selected preset name
  value?: string | null;
  
  // Called when a preset is selected
  onChange?: (preset: Preset | null) => void;
  
  // Optional label for the group (e.g., "Agent A", "Agent B")
  // If not provided, shows "Preset" or the selected preset name
  groupLabel?: string;
  
  // Loading state
  isLoading?: boolean;
  
  // Whether the picker is disabled
  disabled?: boolean;
  
  // Whether streaming is active
  isStreaming?: boolean;
  
  // Additional class names
  className?: string;
}

/**
 * Compact inline preset picker for the chat input bottom bar.
 * Styled to match other inline controls (WorkflowSelector, InlineParamInput).
 */
export function InlinePresetPicker({
  presets,
  value,
  onChange,
  groupLabel,
  isLoading = false,
  disabled = false,
  isStreaming = false,
  className = "",
}: InlinePresetPickerProps) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  // Find the selected preset
  const selectedPreset = presets.find(p => p.name === value);

  // Group presets by source
  const userPresets = presets.filter(p => p.source === "user");
  const builtinPresets = presets.filter(p => p.source === "builtin");
  const projectPresets = presets.filter(p => p.source === "project");

  const handleSelect = (preset: Preset | null) => {
    onChange?.(preset);
    setIsOpen(false);
  };

  const canInteract = !disabled && !isLoading && !isStreaming;

  return (
    <div ref={dropdownRef} className={cn("relative", className)}>
      <Tooltip 
        content={selectedPreset 
          ? `${groupLabel ? groupLabel + ": " : ""}${selectedPreset.name}` 
          : `Select ${groupLabel ? groupLabel + " preset" : "a preset"}`
        } 
        placement="top"
      >
        <button
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-1 rounded transition-colors text-[10px] font-medium h-6 px-2",
            canInteract
              ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
              : "cursor-default opacity-60",
            isOpen
              ? "bg-primary/20 text-primary"
              : isStreaming
              ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]"
          )}
        >
          {isLoading ? (
            <div className="animate-spin rounded-full h-3 w-3 border-b-2 border-current" />
          ) : (
            <>
              <Layers className="w-3 h-3 flex-shrink-0" />
              <span className="truncate max-w-24">
                {selectedPreset 
                  ? selectedPreset.name 
                  : groupLabel || "Preset"
                }
              </span>
              <ChevronDown className="w-2.5 h-2.5 opacity-50 flex-shrink-0" />
            </>
          )}
        </button>
      </Tooltip>

      {/* Dropdown */}
      {isOpen && canInteract && (
        <div
          className={cn(
            "absolute bottom-full left-0 mb-1 min-w-48 rounded-lg border border-border bg-[var(--chat-dropdown-bg)] shadow-lg z-[1000]",
            "max-h-64 overflow-y-auto"
          )}
        >
          <div className="py-1">
            {/* Header showing which group this preset applies to */}
            {groupLabel && (
              <div className="px-3 py-1.5 text-[10px] font-semibold text-muted-foreground border-b border-border/50">
                {groupLabel} Preset
              </div>
            )}

            {/* None Option */}
            <button
              onClick={() => handleSelect(null)}
              className={cn(
                "w-full px-3 py-1.5 text-left text-xs transition-colors",
                !value ? "bg-accent font-medium" : "hover:bg-accent/50"
              )}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-muted-foreground">None (use defaults)</span>
                {!value && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
              </div>
            </button>

            {/* User Presets */}
            {userPresets.length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
                  User
                </div>
                {userPresets.map((preset) => (
                  <PresetOption
                    key={preset.name}
                    preset={preset}
                    isSelected={value === preset.name}
                    onSelect={handleSelect}
                  />
                ))}
              </>
            )}

            {/* Builtin Presets */}
            {builtinPresets.length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
                  Built-in
                </div>
                {builtinPresets.map((preset) => (
                  <PresetOption
                    key={preset.name}
                    preset={preset}
                    isSelected={value === preset.name}
                    onSelect={handleSelect}
                  />
                ))}
              </>
            )}

            {/* Project Presets */}
            {projectPresets.length > 0 && (
              <>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
                  Project
                </div>
                {projectPresets.map((preset) => (
                  <PresetOption
                    key={preset.name}
                    preset={preset}
                    isSelected={value === preset.name}
                    onSelect={handleSelect}
                  />
                ))}
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

interface PresetOptionProps {
  preset: Preset;
  isSelected: boolean;
  onSelect: (preset: Preset) => void;
}

function PresetOption({ preset, isSelected, onSelect }: PresetOptionProps) {
  return (
    <button
      onClick={() => onSelect(preset)}
      className={cn(
        "w-full px-3 py-1.5 text-left text-xs transition-colors",
        isSelected ? "bg-accent font-medium" : "hover:bg-accent/50"
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5">
            <Layers className="w-3 h-3 opacity-50 flex-shrink-0" />
            <span className="font-medium truncate">{preset.name}</span>
          </div>
          {preset.description && (
            <div className="text-[10px] text-muted-foreground mt-0.5 ml-4.5 line-clamp-1">
              {preset.description}
            </div>
          )}
        </div>
        {isSelected && (
          <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
        )}
      </div>
    </button>
  );
}

export default InlinePresetPicker;
