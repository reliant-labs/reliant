import { useState, useEffect, useRef, useCallback } from "react";
import {
  X,
  ChevronRight,
  Box,
  LayoutGrid,
  Settings2,
} from "lucide-react";
import { cn } from "../../../lib/utils";
import { ModelSettingsPage } from "./ModelSettingsPage";
import { PresetSettingsPage } from "./PresetSettingsPage";
import { ParamsSettingsPage } from "./ParamsSettingsPage";
import type { InputDef } from "../../../lib/inputHelpers";
import {
  getInputEnumValues,
  getInputDefault,
  getInputUI,
} from "../../../lib/inputHelpers";
import type { Preset } from "../../../store/globalDataStore";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type PageId = "main" | "model" | "preset" | "params";

interface ChatSettingsPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  initialPage?: PageId;
  // Workflow params (dynamic)
  inputs: Record<string, InputDef> | null;
  values: Record<string, unknown>;
  onChange: (values: Record<string, unknown>) => void;
  // Preset
  presets?: Preset[];
  selectedPreset?: string | null;
  onPresetChange?: (preset: Preset | null) => void;
  // Workflow tag info
  workflowTag?: string;
  groupTags?: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const providerColors: Record<string, string> = {
  anthropic: "#e8945a",
  openai: "#5cb85c",
  codex: "#5cb85c",
  gemini: "#5b9bd5",
  vertexai: "#5b9bd5",
  xai: "#d9534f",
  local: "#a0a0a0",
  openrouter: "#f0ad4e",
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Find the model param key (first key ending with ".model" or typed as model). */
function findModelParamKey(
  inputs: Record<string, InputDef> | null,
): string | null {
  if (!inputs) return null;
  for (const [key, def] of Object.entries(inputs)) {
    if (def?.type === "model") return key;
  }
  return null;
}

/** Find the mode param key (enum with common mode values). */
function findModeParamKey(
  inputs: Record<string, InputDef> | null,
): string | null {
  if (!inputs) return null;
  for (const [key, def] of Object.entries(inputs)) {
    if (def?.type === "enum") {
      const values = getInputEnumValues(def);
      if (
        values &&
        (values.includes("auto") ||
          values.includes("manual") ||
          values.includes("plan"))
      ) {
        return key;
      }
    }
  }
  return null;
}

/** Get the display label for a model value. */
function getModelDisplayLabel(
  value: unknown,
): { label: string; providerColor?: string } {
  if (!value || typeof value !== "object") return { label: "Default" };
  const obj = value as Record<string, unknown>;
  if (Array.isArray(obj.tags) && obj.tags.length > 0) {
    return { label: String(obj.tags[0]) };
  }
  if (obj.id) {
    const id = String(obj.id);
    // Show short name from id
    const parts = id.split("/");
    return { label: parts[parts.length - 1] };
  }
  return { label: "Default" };
}

/** Count extra params beyond model/mode/preset. */
function countExtraParams(
  inputs: Record<string, InputDef> | null,
  modelKey: string | null,
  modeKey: string | null,
): number {
  if (!inputs) return 0;
  let count = 0;
  for (const [key, def] of Object.entries(inputs)) {
    if (key === modelKey || key === modeKey) continue;
    if (def?.type === "preset" || def?.type === "tools") continue;
    const ui = getInputUI(def);
    if (ui === "hidden") continue;
    if (def?.type === "message" || def?.type === "attachments" || def?.type === "thread") continue;
    count++;
  }
  return count;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ChatSettingsPopover({
  isOpen,
  onClose,
  initialPage = "main",
  inputs,
  values,
  onChange,
  presets,
  selectedPreset,
  onPresetChange,
}: ChatSettingsPopoverProps) {
  const [currentPage, setCurrentPage] = useState<PageId>(initialPage);
  const popoverRef = useRef<HTMLDivElement>(null);

  // Reset page when popover opens
  useEffect(() => {
    if (isOpen) {
      setCurrentPage(initialPage);
    }
  }, [isOpen, initialPage]);

  // Close on click outside (but not when clicking the trigger button itself)
  useEffect(() => {
    if (!isOpen) return;
    const handleClick = (e: MouseEvent) => {
      // The popover is rendered inside a relative wrapper alongside its trigger button.
      // Check the wrapper (parentElement) so clicks on the trigger are ignored here —
      // the trigger's own onClick toggle handles open/close.
      const wrapper = popoverRef.current?.parentElement;
      if (wrapper && !wrapper.contains(e.target as Node)) {
        onClose();
      }
    };
    const raf = requestAnimationFrame(() => {
      document.addEventListener("mousedown", handleClick);
    });
    return () => {
      cancelAnimationFrame(raf);
      document.removeEventListener("mousedown", handleClick);
    };
  }, [isOpen, onClose]);

  // Find special param keys
  const modelKey = findModelParamKey(inputs);
  const modeKey = findModeParamKey(inputs);
  const extraParamCount = countExtraParams(inputs, modelKey, modeKey);

  // Current values
  const modelValue = modelKey ? values[modelKey] : undefined;
  const modeValue = modeKey ? (values[modeKey] as string) : undefined;
  const modeOptions = modeKey ? getInputEnumValues(inputs![modeKey]) : null;
  const modeDefault = modeKey ? getInputDefault(inputs![modeKey]) : null;

  // Model display info
  const modelDisplay = getModelDisplayLabel(modelValue);

  // Handlers
  const handleModelChange = useCallback(
    (newValue: unknown) => {
      if (!modelKey) return;
      onChange({ ...values, [modelKey]: newValue });
    },
    [modelKey, values, onChange],
  );

  const handleModeChange = useCallback(
    (mode: string) => {
      if (!modeKey) return;
      onChange({ ...values, [modeKey]: mode });
    },
    [modeKey, values, onChange],
  );

  const handlePresetSelect = useCallback(
    (preset: Preset | null) => {
      onPresetChange?.(preset);
      setCurrentPage("main");
    },
    [onPresetChange],
  );

  if (!isOpen) return null;

  return (
      <div
        ref={popoverRef}
        style={{
          animation: "chatSettingsPopoverIn 0.12s ease-out",
        }}
        className="absolute bottom-[calc(100%+8px)] right-0 z-[100] w-[360px] bg-[#282828] border border-[#404040] rounded-[10px] shadow-[0_8px_32px_rgba(0,0,0,0.5),0_2px_8px_rgba(0,0,0,0.3)] overflow-hidden"
      >
        {/* Main page */}
        {currentPage === "main" && (
          <div>
            {/* Header */}
            <div className="flex items-center px-3 py-2.5 border-b border-[#3a3a3a] gap-2">
              <h3 className="text-[13px] font-semibold text-[#e5e5e5] flex-1">
                Chat Settings
              </h3>
              <button
                onClick={onClose}
                className="w-6 h-6 flex items-center justify-center rounded text-[#707070] hover:text-[#e5e5e5] transition-colors"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>

            {/* Model row */}
            {modelKey && (
              <button
                onClick={() => setCurrentPage("model")}
                className="w-full flex items-center justify-between px-4 py-2.5 text-[13px] text-[#e5e5e5] hover:bg-[#363636] transition-colors text-left"
              >
                <div className="flex items-center gap-2.5">
                  <Box className="w-4 h-4 text-[#a0a0a0] shrink-0" />
                  <span className="font-medium">Model</span>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-[#707070]">
                  {modelDisplay.label !== "Default" && (
                    <span
                      className="w-1.5 h-1.5 rounded-full inline-block shrink-0"
                      style={{
                        backgroundColor:
                          providerColors.anthropic,
                      }}
                    />
                  )}
                  <span>{modelDisplay.label}</span>
                  <ChevronRight className="w-3 h-3 text-[#707070]" />
                </div>
              </button>
            )}

            {/* Preset row */}
            {presets && presets.length > 0 && (
              <button
                onClick={() => setCurrentPage("preset")}
                className="w-full flex items-center justify-between px-4 py-2.5 text-[13px] text-[#e5e5e5] hover:bg-[#363636] transition-colors text-left"
              >
                <div className="flex items-center gap-2.5">
                  <LayoutGrid className="w-4 h-4 text-[#a0a0a0] shrink-0" />
                  <span className="font-medium">Preset</span>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-[#707070]">
                  <span>{selectedPreset || "None"}</span>
                  <ChevronRight className="w-3 h-3 text-[#707070]" />
                </div>
              </button>
            )}

            {/* Divider before mode */}
            {modeKey && modeOptions && (
              <>
                <div className="h-px bg-[#3a3a3a] mx-0 my-1" />

                {/* Mode section */}
                <div className="px-4 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wider text-[#707070]">
                  Execution Mode
                </div>
                <div className="px-4 pb-2.5 flex gap-1.5">
                  {modeOptions.map((mode) => {
                    const currentMode =
                      modeValue ?? (modeDefault as string) ?? modeOptions[0];
                    const isSelected = currentMode === mode;
                    return (
                      <button
                        key={mode}
                        onClick={() => handleModeChange(mode)}
                        className={cn(
                          "flex-1 px-2.5 py-1.5 rounded-md border text-xs font-medium text-center transition-all capitalize",
                          isSelected
                            ? "border-[#7c6ef0] bg-[rgba(124,110,240,0.15)] text-[#7c6ef0]"
                            : "border-[#3a3a3a] bg-transparent text-[#a0a0a0] hover:border-[#4a4a4a] hover:text-[#e5e5e5]",
                        )}
                      >
                        {mode}
                      </button>
                    );
                  })}
                </div>
              </>
            )}

            {/* More Settings row */}
            {extraParamCount > 0 && (
              <>
                {!modeKey && (
                  <div className="h-px bg-[#3a3a3a] mx-0 my-1" />
                )}
                <button
                  onClick={() => setCurrentPage("params")}
                  className="w-full flex items-center justify-between px-4 py-2.5 text-[13px] text-[#e5e5e5] hover:bg-[#363636] transition-colors text-left"
                >
                  <div className="flex items-center gap-2.5">
                    <Settings2 className="w-4 h-4 text-[#a0a0a0] shrink-0" />
                    <span className="font-medium">More Settings</span>
                  </div>
                  <div className="flex items-center gap-1.5 text-xs text-[#707070]">
                    <span className="inline-flex items-center justify-center min-w-4 h-4 px-1 rounded-full bg-[#363636] text-[10px] font-semibold">
                      {extraParamCount}
                    </span>
                    <ChevronRight className="w-3 h-3 text-[#707070]" />
                  </div>
                </button>
              </>
            )}
          </div>
        )}

        {/* Model page */}
        {currentPage === "model" && modelKey && (
          <ModelSettingsPage
            value={modelValue}
            onChange={handleModelChange}
            onBack={() => setCurrentPage("main")}
            onClose={onClose}
          />
        )}

        {/* Preset page */}
        {currentPage === "preset" && presets && (
          <PresetSettingsPage
            presets={presets}
            selectedPreset={selectedPreset ?? null}
            onPresetChange={handlePresetSelect}
            onBack={() => setCurrentPage("main")}
            onClose={onClose}
          />
        )}

        {/* Params page */}
        {currentPage === "params" && inputs && (
          <ParamsSettingsPage
            inputs={inputs}
            values={values}
            onChange={onChange}
            onBack={() => setCurrentPage("main")}
            onClose={onClose}
            excludeParams={[
              ...(modelKey ? [modelKey] : []),
              ...(modeKey ? [modeKey] : []),
            ]}
          />
        )}
      </div>
  );
}