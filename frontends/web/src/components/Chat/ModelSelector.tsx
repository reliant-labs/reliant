import { useState, useEffect, useMemo } from "react";
import { Sparkles } from "lucide-react";
import Combobox, { type ComboboxOption } from "../forge-ui/combobox";
import { useModels } from "../../store/globalDataStore";

interface ModelSelectorProps {
  onSelect: (modelId: string | null) => void;
  defaultModel?: string | null;
  /** Accessible name when no <label htmlFor={id}> names it. Defaults to "Model". */
  "aria-label"?: string;
  id?: string;
}

function ModelSelectorComponent({
  onSelect,
  defaultModel = null,
  "aria-label": ariaLabel,
  id,
}: ModelSelectorProps) {
  const [selectedModel, setSelectedModel] = useState<string | null>(
    defaultModel
  );

  // Use cached models from global store
  const { models, loading } = useModels();

  // Update selected model when defaultModel prop changes
  // Only update if we have a real value change (not just null/undefined flashing)
  // This prevents the "Auto" flash when the parent briefly loses state
  useEffect(() => {
    // If defaultModel is null/undefined and we already have a non-empty selection, keep it
    // This makes the selector "sticky" and prevents flashing during re-renders
    const hasCurrentSelection = selectedModel && selectedModel !== "";
    const isDefaultEmpty =
      defaultModel === null ||
      defaultModel === undefined ||
      defaultModel === "";

    if (isDefaultEmpty && hasCurrentSelection) {
      return; // Keep current selection
    }

    // If we have a real model value and it's different, update
    if (selectedModel !== defaultModel) {
      setSelectedModel(defaultModel);
    }
  }, [defaultModel, selectedModel]);

  // Removed loadModels() - models are now loaded from global store on app startup

  // Get color based on driver ID (the actual API being used)
  const getDriverColor = (driverId: string) => {
    const colors: Record<string, string> = {
      codex: "#10b981", // emerald-500 (ChatGPT Codex)
      anthropic: "#f97316", // orange-500
      claude: "#fb923c", // orange-400 (Claude subscription)
      openai: "#22c55e", // green-500
      gemini: "#3b82f6", // blue-500
      "google ai": "#3b82f6", // blue-500
      mistral: "#a855f7", // purple-500
      meta: "#06b6d4", // cyan-500
      deepseek: "#ef4444", // red-500
      groq: "#facc15", // yellow-500
      reliant: "#2563eb", // blue-600 (Reliant brand blue)
      openrouter: "#8b5cf6", // violet-500
      azure: "#0ea5e9", // sky-500
      bedrock: "#f59e0b", // amber-500
      vertexai: "#10b981", // emerald-500
      "vertex ai": "#10b981", // emerald-500
      copilot: "#1f2937", // gray-800
      "github copilot": "#1f2937", // gray-800
      local: "#d946ef", // fuchsia-500
    };
    return colors[driverId.toLowerCase()] || "#6b7280";
  };

  const handleSelect = (modelId: string | null) => {
    setSelectedModel(modelId);
    onSelect(modelId);
  };

  // "Auto" means the system will select an appropriate model per agent
  const autoDescription = "Tailored per agent";

  // Convert models to dropdown options (useMemo ensures React detects when description changes)
  // Now grouped by provider (the human-readable API name from backend like "Anthropic", "OpenRouter")
  const dropdownOptions = useMemo<ComboboxOption[]>(() => {
    const driverIcon = (driverId: string) => {
      return (
        <div
          className="w-2 h-2 rounded-full flex-shrink-0"
          style={{ backgroundColor: getDriverColor(driverId) }}
        />
      );
    };
    return [
      // Default option
      {
        value: "",
        label: "Auto",
        description: autoDescription,
        icon: <Sparkles className="w-3 h-3 text-muted-foreground" />,
        group: "System",
      },
      // Model options grouped by provider/API
      // The provider field now contains human-readable names like "Anthropic", "OpenRouter"
      ...models.map(
        (model): ComboboxOption => ({
          value: model.id,
          label: model.name,
          // Use driverId for icon color (falls back to provider if driverId not set)
          icon: driverIcon(model.driverId || model.provider),
          // Group by provider (the human-readable API name)
          group: model.provider,
        })
      ),
    ];
  }, [autoDescription, models]);

  return (
    <Combobox
      id={id}
      aria-label={id ? ariaLabel : (ariaLabel ?? "Model")}
      options={dropdownOptions}
      value={selectedModel || ""}
      placeholder={loading ? "Loading models..." : "Select a model"}
      searchPlaceholder="Search models"
      emptyMessage="No models found"
      onChange={(value) => handleSelect(value ? value : null)}
      loading={loading}
    />
  );
}

// Don't use memo - we need to re-render when global preferences change
export const ModelSelector = ModelSelectorComponent;