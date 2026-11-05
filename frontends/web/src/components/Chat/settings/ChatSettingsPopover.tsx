import { useState, useEffect, useCallback, type ReactElement } from "react";
import Popover, { type PopoverTriggerProps } from "../../forge-ui/popover";
import {
  X,
  ChevronRight,
  ChevronLeft,
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
  getInputMulti,
  getInputDefault,
  getInputUI,
} from "../../../lib/inputHelpers";
import type { Preset } from "../../../hooks/preset-queries";
import { isSelectorHidden, selectableOptions } from "../../../lib/executionMode";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type PageId = "main" | "model" | "preset" | "params";

interface ChatSettingsPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  /**
   * Anchored (not embedded): the control that opens the panel. Spread the
   * props onto your own button; the panel is placed against it, flipped and
   * clamped so it never leaves the viewport on any screen.
   */
  trigger?: (props: PopoverTriggerProps) => ReactElement;
  /** Anchored: the trigger asked to open the panel. */
  onOpen?: () => void;
  initialPage?: PageId;
  /**
   * Render as a page inside a host sheet (the composer's ⊕ sheet) instead of
   * as an anchored popover: no dialog shell of its own, no outside-click
   * dismissal, and no close buttons — the host sheet has the one close.
   * Back from the page it opened on returns to the host via `onExit`.
   */
  embedded?: boolean;
  onExit?: () => void;
  disabled?: boolean;
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
  /** The chat these settings belong to; absent when they are a sub-agent's or a new chat's. */
  chatId?: string;
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
  local: "#a0a0a0",
  openrouter: "#f0ad4e",
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Find the model param key (first key ending with ".model" or typed as model). */
export function findModelParamKey(
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
    if (def?.type === "enum" && !getInputMulti(def)) {
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

/** Count extra params beyond model/mode/preset that are not already in the toolbar. */
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
  trigger,
  onOpen,
  initialPage = "main",
  embedded = false,
  onExit,
  disabled = false,
  inputs,
  values,
  onChange: persistValues,
  presets,
  selectedPreset,
  onPresetChange: persistPreset,
  chatId,
}: ChatSettingsPopoverProps) {
  const [currentPage, setCurrentPage] = useState<PageId>(initialPage);

  // A fieldset blocks user input, not child effects (e.g. thinking-level
  // reconciliation after a catalog refresh). Gate every mutation at the boundary.
  const onChange = useCallback((next: Record<string, unknown>) => {
    if (!disabled) persistValues(next);
  }, [disabled, persistValues]);
  const onPresetChange = useCallback((preset: Preset | null) => {
    if (!disabled) persistPreset?.(preset);
  }, [disabled, persistPreset]);

  // Reset page when popover opens
  useEffect(() => {
    if (isOpen) {
      setCurrentPage(initialPage);
    }
  }, [isOpen, initialPage]);

  // Find special param keys
  const modelKey = findModelParamKey(inputs);
  const modeKey = findModeParamKey(inputs);
  const extraParamCount = countExtraParams(inputs, modelKey, modeKey);

  // Current values. An untouched param has no entry in `values`, so fall back to
  // the schema default — the model page derives the available thinking levels
  // from the selected model, and without it every model looks like "no model".
  const modelValue = modelKey
    ? values[modelKey] ?? getInputDefault(inputs![modelKey])
    : undefined;
  const modeValue = modeKey ? (values[modeKey] as string) : undefined;
  const modeOptions = modeKey ? getInputEnumValues(inputs![modeKey]) : null;
  const modeDefault = modeKey ? getInputDefault(inputs![modeKey]) : null;
  const currentMode = modeValue ?? (modeDefault as string) ?? modeOptions?.[0] ?? "";

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

  // `undefined` resets the mode: the key leaves `values`, so the workflow's
  // default (or a preset's mode) applies again.
  const handleModeChange = useCallback(
    (mode: string | undefined) => {
      if (!modeKey) return;
      if (mode === undefined) {
        const { [modeKey]: _reset, ...rest } = values;
        onChange(rest);
        return;
      }
      onChange({ ...values, [modeKey]: mode });
    },
    [modeKey, values, onChange],
  );

  // Back from the page this opened on leaves for the host when embedded (the
  // ⊕ sheet's Model row opens straight onto the model page, and its back
  // arrow means "back to Chat options", not "on to Chat settings").
  const back = useCallback(() => {
    if (embedded && onExit && currentPage === initialPage) onExit();
    else setCurrentPage("main");
  }, [embedded, onExit, currentPage, initialPage]);

  const handlePresetSelect = useCallback(
    (preset: Preset | null) => {
      onPresetChange?.(preset);
      back();
    },
    [onPresetChange, back],
  );

  // One page tree owns all settings and policy; only the surrounding shell
  // varies. `closePanel` is the shell's close: anchored, it is the Popover's,
  // which also hands focus back to the trigger.
  const renderContent = (closePanel: () => void) => (
      <fieldset disabled={disabled} className="min-w-0 border-0 p-0 m-0">
        {/* Main page */}
        {currentPage === "main" && (
          <div>
            {/* Header */}
            <div className="flex items-center px-3 py-2.5 border-b border-border/50 gap-2">
              {embedded && onExit && (
                <button
                  onClick={onExit}
                  aria-label="Back"
                  className="touch-target w-6 h-6 flex items-center justify-center rounded text-muted-foreground hover:text-foreground transition-colors"
                >
                  <ChevronLeft className="w-3.5 h-3.5" />
                </button>
              )}
              <h3 className="text-sm font-semibold text-foreground flex-1">
                Chat Settings
              </h3>
              {!embedded && (
                <button
                  onClick={closePanel}
                  aria-label="Close settings"
                  className="touch-target w-6 h-6 flex items-center justify-center rounded text-muted-foreground hover:text-foreground transition-colors"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
            </div>

            {/* Model row */}
            {modelKey && (
              <button
                onClick={() => setCurrentPage("model")}
                className="w-full flex items-center justify-between px-4 py-2.5 text-sm text-foreground hover:bg-muted/50 transition-colors touch:min-h-[48px] text-left"
              >
                <div className="flex items-center gap-2.5">
                  <Box className="w-4 h-4 text-muted-foreground shrink-0" />
                  <span className="font-medium">Model</span>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground/70">
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
                  <ChevronRight className="w-3 h-3 text-muted-foreground/70" />
                </div>
              </button>
            )}

            {/* Preset row */}
            {presets && presets.length > 0 && (
              <button
                onClick={() => setCurrentPage("preset")}
                className="w-full flex items-center justify-between px-4 py-2.5 text-sm text-foreground hover:bg-muted/50 transition-colors touch:min-h-[48px] text-left"
              >
                <div className="flex items-center gap-2.5">
                  <LayoutGrid className="w-4 h-4 text-muted-foreground shrink-0" />
                  <span className="font-medium">Preset</span>
                </div>
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground/70">
                  <span>{selectedPreset || "None"}</span>
                  <ChevronRight className="w-3 h-3 text-muted-foreground/70" />
                </div>
              </button>
            )}

            {/* Divider before mode */}
            {modeKey && modeOptions && !isSelectorHidden(modeOptions) && (
              <>
                <div className="h-px bg-border/50 mx-0 my-1" />

                {/* Mode section */}
                <div className="px-4 pt-2 pb-1 text-2xs font-semibold uppercase tracking-wider text-muted-foreground/70">
                  Execution Mode
                </div>
                <div className="px-4 pb-2.5 flex gap-1.5">
                  {/* "auto" is not listed, so "default" is the explicit way back
                      to the parameter's default: it drops the override. */}
                  <button
                    onClick={() => handleModeChange(undefined)}
                    className={cn(
                      "flex-1 px-2.5 py-1.5 rounded-md border text-xs font-medium text-center transition-all capitalize touch:min-h-[44px]",
                      !selectableOptions(modeOptions).includes(currentMode)
                        ? "border-primary bg-primary/15 text-primary-ink"
                        : "border-border bg-transparent text-muted-foreground hover:border-border/80 hover:text-foreground",
                    )}
                  >
                    default
                  </button>
                  {selectableOptions(modeOptions).map((mode) => {
                    const isSelected = currentMode === mode;
                    return (
                      <button
                        key={mode}
                        onClick={() => handleModeChange(isSelected ? undefined : mode)}
                        className={cn(
                          "flex-1 px-2.5 py-1.5 rounded-md border text-xs font-medium text-center transition-all capitalize touch:min-h-[44px]",
                          isSelected
                            ? "border-primary bg-primary/15 text-primary-ink"
                            : "border-border bg-transparent text-muted-foreground hover:border-border/80 hover:text-foreground",
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
                  <div className="h-px bg-border/50 mx-0 my-1" />
                )}
                <button
                  onClick={() => setCurrentPage("params")}
                  className="w-full flex items-center justify-between px-4 py-2.5 text-sm text-foreground hover:bg-muted/50 transition-colors touch:min-h-[48px] text-left"
                >
                  <div className="flex items-center gap-2.5">
                    <Settings2 className="w-4 h-4 text-muted-foreground shrink-0" />
                    <span className="font-medium">More Settings</span>
                  </div>
                  <div className="flex items-center gap-1.5 text-xs text-muted-foreground/70">
                    <span className="inline-flex items-center justify-center min-w-4 h-4 px-1 rounded-full bg-muted text-2xs font-semibold">
                      {extraParamCount}
                    </span>
                    <ChevronRight className="w-3 h-3 text-muted-foreground/70" />
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
            onBack={back}
            onClose={closePanel}
            hideClose={embedded}
            chatId={chatId}
          />
        )}

        {/* Preset page */}
        {currentPage === "preset" && presets && (
          <PresetSettingsPage
            presets={presets}
            selectedPreset={selectedPreset ?? null}
            onPresetChange={handlePresetSelect}
            onBack={back}
            onClose={closePanel}
            hideClose={embedded}
          />
        )}

        {/* Params page */}
        {currentPage === "params" && inputs && (
          <ParamsSettingsPage
            inputs={inputs}
            values={values}
            onChange={onChange}
            onBack={back}
            onClose={closePanel}
            hideClose={embedded}
            excludeParams={[
              ...(modelKey ? [modelKey] : []),
              ...(modeKey ? [modeKey] : []),
            ]}
          />
        )}
      </fieldset>
  );

  if (embedded) return isOpen ? renderContent(onClose) : null;
  // Anchored: the shared Popover places the panel (above the composer when it
  // fits, flipped and clamped to the viewport otherwise), caps its width at
  // the viewport's and dismisses it. The height cap scrolls a tall page
  // inside the panel instead of pushing it off a short screen.
  return (
    <Popover
      trigger={trigger}
      open={isOpen}
      onOpenChange={(next) => (next ? onOpen?.() : onClose())}
      side="top"
      label="Chat settings"
      panelClassName="w-[360px] max-h-[calc(100dvh-16px)] overflow-y-auto overscroll-contain"
    >
      {({ close }) => renderContent(close)}
    </Popover>
  );
}