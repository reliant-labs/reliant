// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect } from "react";
import { createPortal } from "react-dom";
import { Layers, ChevronDown, Check, MoreVertical, Star, Edit, Trash2, Save } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import type { Preset } from "../../store/globalDataStore";
import { presetGrpc } from "../../api/preset-grpc";
import { useGlobalDataStore } from "../../store/globalDataStore";

interface PresetPickerProps {
  // Available presets (filtered for the current workflow)
  presets: Preset[];

  // Currently selected preset name
  value?: string | null;

  // Called when a preset is selected
  onChange?: (preset: Preset | null) => void;

  // Loading state
  isLoading?: boolean;

  // Whether the picker is disabled
  disabled?: boolean;

  // Additional class names
  className?: string;

  // Project ID (for preset operations)
  projectId?: string;

  // Workflow name (for default preset)
  workflowName?: string;

  // Optional group name (for group-level default presets)
  groupName?: string;

  // Called when the save button is clicked (shows save icon if provided)
  onSave?: () => void;

  // Tooltip for save button
  saveTooltip?: string;
}

export function PresetPicker({
  presets,
  value,
  onChange,
  isLoading = false,
  disabled = false,
  className = "",
  projectId,
  workflowName,
  groupName,
  onSave,
  saveTooltip = "Save as preset",
}: PresetPickerProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [defaultPresetName, setDefaultPresetName] = useState<string | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const [dropdownPosition, setDropdownPosition] = useState<{ top: number; left: number; width: number } | null>(null);
  const { refetchPresets } = useGlobalDataStore();

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      // Check if click is outside both the dropdown and the button
      if (
        dropdownRef.current && !dropdownRef.current.contains(target) &&
        buttonRef.current && !buttonRef.current.contains(target)
      ) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  // Calculate dropdown position when opening
  useEffect(() => {
    if (isOpen && buttonRef.current) {
      const rect = buttonRef.current.getBoundingClientRect();
      const viewportHeight = window.innerHeight;
      const dropdownMaxHeight = 280; // max-h-64 = 256px + some padding
      
      // Check if there's enough space below
      const spaceBelow = viewportHeight - rect.bottom;
      const showAbove = spaceBelow < dropdownMaxHeight && rect.top > spaceBelow;
      
      setDropdownPosition({
        top: showAbove ? rect.top - dropdownMaxHeight : rect.bottom + 4,
        left: rect.left,
        width: rect.width,
      });
    } else {
      setDropdownPosition(null);
    }
  }, [isOpen]);

  // Fetch default preset for this group when workflow/group changes
  useEffect(() => {
    if (projectId && workflowName) {
      presetGrpc.getDefaultPresets(projectId, workflowName)
        .then((defaults) => {
          // Get default for this specific group (empty string for top-level)
          const defaultName = defaults[groupName || ""] || null;
          setDefaultPresetName(defaultName);
        })
        .catch(() => setDefaultPresetName(null));
    }
  }, [projectId, workflowName, groupName]);

  // Find the selected preset
  const selectedPreset = presets.find(p => p.name === value);

  // Group presets by source and sort alphabetically
  const userPresets = presets
    .filter(p => p.source === "user")
    .sort((a, b) => a.name.localeCompare(b.name));
  const projectPresets = presets
    .filter(p => p.source === "project")
    .sort((a, b) => a.name.localeCompare(b.name));
  const builtinPresets = presets
    .filter(p => p.source === "builtin")
    .sort((a, b) => a.name.localeCompare(b.name));

  const handleSelect = (preset: Preset | null) => {
    onChange?.(preset);
    setIsOpen(false);
  };

  const handleSetDefault = async (preset: Preset) => {
    if (!projectId || !workflowName) return;

    try {
      // Set default for this specific group (empty string for top-level)
      await presetGrpc.setDefaultPreset(projectId, workflowName, groupName || "", preset.name);
      setDefaultPresetName(preset.name);
    } catch (error) {
      console.error("Failed to set default preset:", error);
      alert("Failed to set default preset");
    }
  };

  const handleEdit = async (preset: Preset, newName: string, newDescription: string) => {
    if (!projectId) return;

    try {
      const result = await presetGrpc.updatePreset(projectId, preset.name, {
        newName,
        newDescription,
      });

      if (result.success) {
        await refetchPresets(projectId);
        // If the edited preset is currently selected, update the selection
        if (value === preset.name && result.preset) {
          onChange?.(result.preset);
        }
      } else {
        alert(result.error || "Failed to update preset");
      }
    } catch (error) {
      console.error("Failed to update preset:", error);
      alert("Failed to update preset");
    }
  };

  const handleDelete = async (preset: Preset) => {
    if (!projectId) return;

    if (!confirm(`Delete preset "${preset.name}"? This cannot be undone.`)) {
      return;
    }

    try {
      const result = await presetGrpc.deletePreset(projectId, preset.name);

      if (result.success) {
        await refetchPresets(projectId);
        // If the deleted preset is currently selected, clear selection
        if (value === preset.name) {
          onChange?.(null);
        }
      } else {
        alert(result.error || "Failed to delete preset");
      }
    } catch (error) {
      console.error("Failed to delete preset:", error);
      alert("Failed to delete preset");
    }
  };

  const canInteract = !disabled && !isLoading;

  // Render dropdown content
  const dropdownContent = isOpen && canInteract && dropdownPosition && (
    <div
      ref={dropdownRef}
      className="fixed z-[9999] rounded-md border border-border bg-card shadow-lg"
      style={{
        top: dropdownPosition.top,
        left: dropdownPosition.left,
        width: dropdownPosition.width,
        minWidth: '200px',
      }}
    >
      <div className="py-1 max-h-64 overflow-y-auto">
        {/* None Option */}
        <button
          onClick={() => handleSelect(null)}
          className={cn(
            "w-full px-3 py-2 text-left text-sm transition-colors",
            !value ? "bg-accent font-medium" : "hover:bg-accent/50"
          )}
        >
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">None (use defaults)</span>
            {!value && <Check className="w-4 h-4 text-primary flex-shrink-0" />}
          </div>
        </button>

        {/* User Presets (highest priority) */}
        {userPresets.length > 0 && (
          <>
            <div className="px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
              My Presets
            </div>
            {userPresets.map((preset) => (
              <PresetOption
                key={preset.name}
                preset={preset}
                isSelected={value === preset.name}
                isDefault={defaultPresetName === preset.name}
                onSelect={handleSelect}
                onSetDefault={handleSetDefault}
                onEdit={handleEdit}
                onDelete={handleDelete}
              />
            ))}
          </>
        )}

        {/* Project Presets */}
        {projectPresets.length > 0 && (
          <>
            <div className="px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
              Project
            </div>
            {projectPresets.map((preset) => (
              <PresetOption
                key={preset.name}
                preset={preset}
                isSelected={value === preset.name}
                isDefault={defaultPresetName === preset.name}
                onSelect={handleSelect}
                onSetDefault={handleSetDefault}
                onEdit={handleEdit}
                onDelete={handleDelete}
              />
            ))}
          </>
        )}

        {/* Builtin Presets */}
        {builtinPresets.length > 0 && (
          <>
            <div className="px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-t border-border/30">
              Built-in
            </div>
            {builtinPresets.map((preset) => (
              <PresetOption
                key={preset.name}
                preset={preset}
                isSelected={value === preset.name}
                isDefault={defaultPresetName === preset.name}
                onSelect={handleSelect}
                onSetDefault={handleSetDefault}
                onEdit={handleEdit}
                onDelete={handleDelete}
              />
            ))}
          </>
        )}

        {/* Empty State */}
        {presets.length === 0 && !isLoading && (
          <div className="px-3 py-4 text-sm text-muted-foreground text-center">
            No presets available for this workflow
          </div>
        )}
      </div>
    </div>
  );

  return (
    <div className={cn("relative flex items-center gap-2", className)}>
      {/* Selector Button */}
      <Tooltip content={selectedPreset ? `Preset: ${selectedPreset.name}` : "Select a preset"} placement="top">
        <button
          ref={buttonRef}
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium flex-1",
            "border border-border/50 transition-colors",
            canInteract
              ? "cursor-pointer hover:bg-accent hover:border-border"
              : "cursor-default opacity-60",
            selectedPreset
              ? "bg-accent/50 text-foreground"
              : "bg-background text-muted-foreground"
          )}
        >
          {isLoading ? (
            <>
              <div className="animate-spin rounded-full h-4 w-4 border-b-2 border-current" />
              <span className="flex-1 text-left truncate">Loading presets...</span>
            </>
          ) : (
            <>
              <Layers className="w-4 h-4 flex-shrink-0" />
              <span className="flex-1 text-left truncate">
                {selectedPreset ? selectedPreset.name : "Select a preset..."}
              </span>
              {canInteract && <ChevronDown className="w-4 h-4 opacity-50 flex-shrink-0" />}
            </>
          )}
        </button>
      </Tooltip>

      {/* Save Button */}
      {onSave && (
        <Tooltip content={saveTooltip} placement="top">
          <button
            type="button"
            onClick={onSave}
            disabled={disabled}
            className={cn(
              "p-2 rounded-md border border-border/50 transition-colors",
              "text-muted-foreground hover:text-foreground hover:bg-accent hover:border-border",
              disabled && "opacity-60 cursor-default"
            )}
          >
            <Save className="w-4 h-4" />
          </button>
        </Tooltip>
      )}

      {/* Dropdown rendered via portal */}
      {dropdownContent && createPortal(dropdownContent, document.body)}
    </div>
  );
}

interface PresetOptionProps {
  preset: Preset;
  isSelected: boolean;
  isDefault: boolean;
  onSelect: (preset: Preset) => void;
  onSetDefault: (preset: Preset) => void;
  onEdit: (preset: Preset, newName: string, newDescription: string) => void;
  onDelete: (preset: Preset) => void;
}

function PresetOption({
  preset,
  isSelected,
  isDefault,
  onSelect,
  onSetDefault,
  onEdit,
  onDelete
}: PresetOptionProps) {
  const [showActions, setShowActions] = useState(false);
  const [isEditing, setIsEditing] = useState(false);
  const [editName, setEditName] = useState(preset.name);
  const [editDescription, setEditDescription] = useState(preset.description);
  const actionsRef = useRef<HTMLDivElement>(null);

  // Only user presets can be edited/deleted here; project and builtin presets are read-only.
  const isEditablePreset = preset.source === "user";

  // Close actions menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (actionsRef.current && !actionsRef.current.contains(event.target as Node)) {
        setShowActions(false);
      }
    };

    if (showActions) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [showActions]);

  const handleSaveEdit = () => {
    if (!editName.trim()) {
      alert("Preset name cannot be empty");
      return;
    }
    onEdit(preset, editName, editDescription);
    setIsEditing(false);
  };

  const handleCancelEdit = () => {
    setEditName(preset.name);
    setEditDescription(preset.description);
    setIsEditing(false);
  };

  if (isEditing) {
    // Inline edit mode
    return (
      <div className="px-3 py-2 space-y-2 bg-accent/20 border-b border-border/20">
        <input
          type="text"
          value={editName}
          onChange={(e) => setEditName(e.target.value)}
          className="w-full px-2 py-1 text-sm rounded border border-border bg-background focus:outline-none focus:ring-1 focus:ring-ring"
          placeholder="Preset name"
          autoFocus
          onKeyDown={(e) => {
            if (e.key === "Enter") handleSaveEdit();
            if (e.key === "Escape") handleCancelEdit();
          }}
        />
        <input
          type="text"
          value={editDescription}
          onChange={(e) => setEditDescription(e.target.value)}
          className="w-full px-2 py-1 text-sm rounded border border-border bg-background focus:outline-none focus:ring-1 focus:ring-ring"
          placeholder="Description"
        />
        <div className="flex gap-2">
          <button
            onClick={handleSaveEdit}
            className="px-2 py-1 text-xs rounded bg-primary text-primary-foreground hover:bg-primary/90"
          >
            Save
          </button>
          <button
            onClick={handleCancelEdit}
            className="px-2 py-1 text-xs rounded border border-border hover:bg-accent"
          >
            Cancel
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="relative group">
      <button
        onClick={() => onSelect(preset)}
        className={cn(
          "w-full px-3 py-2 text-left text-sm transition-colors border-b border-border/20",
          isSelected ? "bg-accent font-medium" : "hover:bg-accent/50"
        )}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <Layers className="w-3.5 h-3.5 opacity-50 flex-shrink-0" />
              <span className="font-medium truncate">{preset.name}</span>
              {isDefault && (
                <Star className="w-3 h-3 text-yellow-500 fill-yellow-500 flex-shrink-0" />
              )}
            </div>
            {preset.description && (
              <div className="text-xs text-muted-foreground mt-0.5 ml-5.5 line-clamp-2">
                {preset.description}
              </div>
            )}
          </div>
          <div className="flex items-center gap-1 flex-shrink-0">
            {isSelected && (
              <Check className="w-4 h-4 text-primary" />
            )}
            <button
              onClick={(e) => {
                e.stopPropagation();
                setShowActions(!showActions);
              }}
              className="p-1 rounded opacity-0 group-hover:opacity-100 hover:bg-accent/50 transition-opacity"
            >
              <MoreVertical className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      </button>

      {/* Actions Dropdown */}
      {showActions && (
        <div
          ref={actionsRef}
          className="absolute right-0 top-full mt-1 z-[110] rounded-md border border-border bg-card shadow-lg py-1 min-w-[140px]"
        >
          <button
            onClick={() => {
              onSetDefault(preset);
              setShowActions(false);
            }}
            className="w-full px-3 py-1.5 text-left text-xs hover:bg-accent transition-colors flex items-center gap-2"
          >
            <Star className="w-3.5 h-3.5" />
            Set as Default
          </button>
          {isEditablePreset && (
            <>
              <button
                onClick={() => {
                  setIsEditing(true);
                  setShowActions(false);
                }}
                className="w-full px-3 py-1.5 text-left text-xs hover:bg-accent transition-colors flex items-center gap-2"
              >
                <Edit className="w-3.5 h-3.5" />
                Edit
              </button>
              <button
                onClick={() => {
                  onDelete(preset);
                  setShowActions(false);
                }}
                className="w-full px-3 py-1.5 text-left text-xs hover:bg-destructive/10 text-destructive transition-colors flex items-center gap-2"
              >
                <Trash2 className="w-3.5 h-3.5" />
                Delete
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}

export default PresetPicker;
