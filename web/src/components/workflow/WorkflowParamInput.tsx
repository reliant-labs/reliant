// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect, useMemo } from "react";
import { cn } from "../../lib/utils";
import { useModels, useGlobalDataStore } from "../../store/globalDataStore";
import { useViewerStore } from "../../store/viewerStore";
import { logger } from "../../lib/logger";
import { hasDefaultValue, formatValueForDisplay } from "../../lib/paramUtils";
import { ChevronDown, Check, Info, Loader2 } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { DynamicInput } from "./DynamicInput";
import type { ParamSuggestion, NodeSuggestion } from "./DynamicInput";
import { ObjectSchemaEditor } from "./ObjectSchemaEditor";
import { useThinkingCapability, reconcileThinkingLevel } from "../../hooks/useThinkingCapability";
import { useEvent } from "../../lib/event-context";

import type { InputDef } from "../../lib/inputHelpers";
import {
  getInputDescription,
  getInputDefault,
  getInputEnumValues,
  getInputMulti,
  getInputMin,
  getInputMax,
  getInputProperties,
} from "../../lib/inputHelpers";

// JSON Schema property definition
export interface PropertySchema {
  type: string;
  description?: string;
  enum?: (string | number | boolean)[];
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
  properties?: Record<string, PropertySchema>; // For nested objects
  items?: PropertySchema; // For array items
  required?: string[]; // For nested objects
}

// Helper to check if user must provide a value (no default)
export function isInputRequired(schema: InputDef): boolean {
  const d = getInputDefault(schema);
  return d === undefined || d === null || d === '';
}

const PARAM_CONTROL_BASE_CLASS =
  "rounded-lg border border-border/50 bg-background/80 text-sm text-foreground shadow-sm shadow-black/5 transition-all placeholder:text-muted-foreground/60 hover:border-border hover:bg-background focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20";
const PARAM_CONTROL_CLASS = `w-full px-3 py-2 ${PARAM_CONTROL_BASE_CLASS}`;
const PARAM_COMPACT_CONTROL_CLASS = `px-2.5 py-1.5 ${PARAM_CONTROL_BASE_CLASS}`;
const PARAM_SELECT_CLASS =
  "flex w-full items-center justify-between gap-2 rounded-lg border border-border/50 bg-background/80 px-3 py-2 text-sm text-foreground shadow-sm shadow-black/5 transition-all hover:border-border hover:bg-accent/30 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20";
const PARAM_DROPDOWN_CLASS =
  "absolute left-0 right-0 top-full mt-1.5 overflow-hidden rounded-xl border border-border/50 shadow-xl shadow-black/10 backdrop-blur-sm";
const PARAM_DROPDOWN_ITEM_CLASS =
  "w-full px-3 py-2 text-left text-sm text-foreground transition-colors hover:bg-accent/60";

function getDropdownSurfaceClass(isChatInputContext: boolean): string {
  return isChatInputContext
    ? "z-[1000] bg-[var(--chat-dropdown-bg)]"
    : "z-50 bg-popover/95";
}

interface WorkflowParamInputProps {
  // Parameter name
  name: string;

  // Parameter schema
  schema: InputDef;

  // Current value
  value: unknown;

  // Called when value changes
  onChange: (value: unknown) => void;

  // Whether input is disabled
  disabled?: boolean;

  // Additional class names
  className?: string;

  // Available workflow input params for CEL suggestions
  availableParams?: ParamSuggestion[];

  // Available node outputs for CEL suggestions
  availableNodes?: NodeSuggestion[];

  // Hide the CEL toggle (for contexts where CEL doesn't apply, like chat params)
  hideCELToggle?: boolean;

  // Full form values for model-aware enum filtering (e.g. thinking_level)
  formValues?: Record<string, unknown>;

  // When true, dropdowns should use chat-input layering and opaque chat surfaces
  isChatInputContext?: boolean;
}

export function WorkflowParamInput({
  name,
  schema,
  value,
  onChange,
  disabled = false,
  className = "",
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
  formValues,
  isChatInputContext = false,
}: WorkflowParamInputProps) {
  const previousModelIdRef = useRef<string | undefined>(undefined);
  const thinkingCapability = useThinkingCapability(name, formValues);

  const effectiveSchema = useMemo(() => {
    if (schema.type !== "enum") return schema;
    if (!(name === "thinking_level" || name.endsWith(".thinking_level"))) return schema;

    if (!thinkingCapability.modelId || !thinkingCapability.supportsThinking) {
      return {
        ...schema,
        enum: [],
      };
    }

    const schemaOptions = getInputEnumValues(schema) || [];

    let filtered: string[] = [...thinkingCapability.levels];
    if (schemaOptions.length > 0) {
      const schemaSet = new Set(schemaOptions);
      filtered = filtered.filter((option) => schemaSet.has(option));
    }

    return {
      ...schema,
      enum: filtered,
    };
  }, [schema, name, thinkingCapability]);

  // Keep thinking_level value valid when model changes.
  // Example: codex/xhigh -> gemini (no xhigh) should auto-fallback immediately.
  useEffect(() => {
    if (effectiveSchema.type !== "enum") return;
    if (!(name === "thinking_level" || name.endsWith(".thinking_level"))) return;

    const options = getInputEnumValues(effectiveSchema) || [];
    const current = typeof value === "string" ? value : "";
    const modelChanged = previousModelIdRef.current !== thinkingCapability.modelId;
    previousModelIdRef.current = thinkingCapability.modelId;

    if (current && options.includes(current)) return;

    // If user switched to a reasoning-capable model and current is off,
    // default to medium when available, otherwise highest available.
    if (!current && options.length > 0) {
      if (modelChanged) {
        const preferred = reconcileThinkingLevel("", thinkingCapability);
        onChange(preferred);
      }
      return;
    }

    if (options.length === 0) {
      if (current !== "") {
        onChange("");
      }
      return;
    }

    const fallback = reconcileThinkingLevel(current, thinkingCapability);
    if (fallback !== current) {
      onChange(fallback);
    }
  }, [effectiveSchema, name, onChange, value, thinkingCapability]);

  // Render appropriate input based on semantic type
  switch (effectiveSchema.type) {
    case "boolean":
      return (
        <BooleanInput
          name={name}
          schema={effectiveSchema}
          value={value as boolean}
          onChange={onChange}
          disabled={disabled}
          className={className}
          availableParams={availableParams}
          availableNodes={availableNodes}
          hideCELToggle={hideCELToggle}
        />
      );

    case "number":
    case "integer":
      return (
        <NumberInput
          name={name}
          schema={effectiveSchema}
          value={value as number}
          onChange={(val) => onChange(val)}
          disabled={disabled}
          className={className}
          availableParams={availableParams}
          availableNodes={availableNodes}
          hideCELToggle={hideCELToggle}
        />
      );

    case "enum":
      // Check if multi-select enum
      if (getInputMulti(schema)) {
        return (
          <MultiEnumInput
            name={name}
            schema={effectiveSchema}
            value={value as string[]}
            onChange={onChange}
            disabled={disabled}
            className={className}
            availableParams={availableParams}
            availableNodes={availableNodes}
            hideCELToggle={hideCELToggle}
            isChatInputContext={isChatInputContext}
          />
        );
      }
      return (
        <EnumInput
          name={name}
          schema={effectiveSchema}
          value={value as string}
          onChange={onChange}
          disabled={disabled}
          className={className}
          availableParams={availableParams}
          availableNodes={availableNodes}
          hideCELToggle={hideCELToggle}
          isChatInputContext={isChatInputContext}
        />
      );

    case "model":
      return (
        <ModelInput
          name={name}
          schema={effectiveSchema}
          value={value as string}
          onChange={onChange}
          disabled={disabled}
          className={className}
          availableParams={availableParams}
          availableNodes={availableNodes}
          hideCELToggle={hideCELToggle}
          isChatInputContext={isChatInputContext}
        />
      );

    case "tools":
      // Tools input is handled by a separate ToolsSelector component
      return null;

    case "preset":
      // Preset input is handled by PresetPicker at the top of the panel
      return null;

    case "message":
    case "attachments":
      // These are primary inputs, not shown in params panel
      return null;

    case "object":
      // If schema has properties, use ObjectSchemaEditor
      if (getInputProperties(effectiveSchema)) {
        return (
          <ParamWrapper
            name={name}
            description={getInputDescription(effectiveSchema)}
            hasDefault={getInputDefault(effectiveSchema) !== undefined}
            className={className}
          >
            <ObjectSchemaEditor
              schema={effectiveSchema}
              value={value}
              onChange={onChange}
              disabled={disabled}
            />
          </ParamWrapper>
        );
      }
      // Otherwise fall through to string input for JSON editing
      return (
        <StringInput
          name={name}
          schema={effectiveSchema}
          value={typeof value === "object" ? JSON.stringify(value, null, 2) : (value as string)}
          onChange={(val) => {
            try {
              onChange(JSON.parse(val));
            } catch {
              onChange(val);
            }
          }}
          disabled={disabled}
          className={className}
        />
      );

    case "string":
    default:
      return (
        <StringInput
          name={name}
          schema={effectiveSchema}
          value={value as string}
          onChange={onChange}
          disabled={disabled}
          className={className}
        />
      );
  }
}

// ============================================
// Boolean Input (Toggle) - with CEL expression support
// ============================================

interface BooleanInputProps {
  name: string;
  schema: InputDef;
  value: unknown; // Can be boolean or CEL expression string
  onChange: (value: unknown) => void;
  disabled?: boolean;
  className?: string;
  availableParams?: ParamSuggestion[];
  availableNodes?: NodeSuggestion[];
  hideCELToggle?: boolean;
}

function BooleanInput({ 
  name, 
  schema, 
  value, 
  onChange, 
  disabled = false, 
  className,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
}: BooleanInputProps) {
  return (
    <ParamWrapper
      name={name}
      description={getInputDescription(schema)}
      hasDefault={getInputDefault(schema) !== undefined}
      className={className}
      inline
    >
      <DynamicInput
        type="boolean"
        value={value}
        onChange={onChange}
        disabled={disabled}
        availableParams={availableParams}
        availableNodes={availableNodes}
        hideCELToggle={hideCELToggle}
        renderNative={({ value: nativeValue, onChange: nativeOnChange, disabled: nativeDisabled }) => {
          const isOn = (nativeValue as boolean) ?? getInputDefault(schema) ?? false;
          return (
            <button
              type="button"
              onClick={() => !nativeDisabled && nativeOnChange(!isOn)}
              disabled={nativeDisabled}
              className={cn(
                "relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border border-border/40 p-0.5 shadow-inner transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/25",
                isOn ? "bg-primary" : "bg-muted/70 hover:bg-muted",
                nativeDisabled && "opacity-50 cursor-not-allowed"
              )}
            >
              <span
                className={cn(
                  "pointer-events-none inline-block h-5 w-5 transform rounded-full bg-background shadow-md ring-0 transition-transform",
                  isOn ? "translate-x-5" : "translate-x-0"
                )}
              />
            </button>
          );
        }}
      />
    </ParamWrapper>
  );
}

// ============================================
// Number Input (with optional slider) - with CEL expression support
// ============================================

interface NumberInputProps {
  name: string;
  schema: InputDef;
  value: unknown; // Can be number or CEL expression string
  onChange: (value: unknown) => void;
  disabled?: boolean;
  className?: string;
  availableParams?: ParamSuggestion[];
  availableNodes?: NodeSuggestion[];
  hideCELToggle?: boolean;
}

function NumberInput({ 
  name, 
  schema, 
  value, 
  onChange, 
  disabled = false, 
  className,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
}: NumberInputProps) {
  const hasRange = getInputMin(schema) !== undefined && getInputMax(schema) !== undefined;

  return (
    <ParamWrapper 
      name={name} 
      description={getInputDescription(schema)} 
      hasDefault={getInputDefault(schema) !== undefined}
      className={className}
    >
      <DynamicInput
        type="number"
        value={value}
        onChange={onChange}
        disabled={disabled}
        availableParams={availableParams}
        availableNodes={availableNodes}
        hideCELToggle={hideCELToggle}
        renderNative={({ value: nativeValue, onChange: nativeOnChange, disabled: nativeDisabled }) => {
          const currentValue = (nativeValue as number) ?? getInputDefault(schema) ?? (getInputMin(schema) ?? 0);
          
          // Handle direct input change with validation
          const handleInputChange = (inputValue: string) => {
            const parsed = parseFloat(inputValue);
            if (isNaN(parsed)) return;
            let clamped = parsed;
            if (getInputMin(schema) !== undefined) clamped = Math.max(getInputMin(schema)!, clamped);
            if (getInputMax(schema) !== undefined) clamped = Math.min(getInputMax(schema)!, clamped);
            nativeOnChange(clamped);
          };

          if (hasRange) {
            const min = getInputMin(schema)!;
            const max = getInputMax(schema)!;
            const step = schema.type === "integer" ? 1 : 0.1;
            const percentage = ((currentValue - min) / (max - min)) * 100;

            return (
              <div className="flex items-center gap-3 flex-1">
                <input
                  type="range"
                  min={min}
                  max={max}
                  step={step}
                  value={currentValue}
                  onChange={(e) => nativeOnChange(parseFloat(e.target.value))}
                  disabled={nativeDisabled}
                  className={cn(
                    "h-2 flex-1 cursor-pointer appearance-none rounded-full bg-muted/60 accent-primary",
                    "[&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:appearance-none",
                    "[&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:border-2 [&::-webkit-slider-thumb]:border-background [&::-webkit-slider-thumb]:bg-primary",
                    "[&::-webkit-slider-thumb]:cursor-pointer [&::-webkit-slider-thumb]:shadow-md",
                    nativeDisabled && "opacity-50 cursor-not-allowed"
                  )}
                  style={{
                    background: `linear-gradient(to right, hsl(var(--primary)) ${percentage}%, hsl(var(--muted)) ${percentage}%)`
                  }}
                />
                <input
                  type="number"
                  min={min}
                  max={max}
                  step={step}
                  value={schema.type === "integer" ? Math.round(currentValue) : currentValue}
                  onChange={(e) => handleInputChange(e.target.value)}
                  disabled={nativeDisabled}
                  className={cn(
                    PARAM_COMPACT_CONTROL_CLASS,
                    "w-20 text-right tabular-nums",
                    nativeDisabled && "opacity-50 cursor-not-allowed"
                  )}
                />
              </div>
            );
          }

          // Plain number input
          const numValue = typeof nativeValue === "number" ? nativeValue : undefined;
          const numDefault = typeof getInputDefault(schema) === "number" ? getInputDefault(schema) : undefined;
          const displayValue = numValue !== undefined ? numValue : (numDefault !== undefined ? numDefault : "");

          return (
            <input
              type="number"
              value={displayValue === "" ? "" : displayValue as string | number | undefined}
              onChange={(e) => {
                const val = e.target.value;
                if (val === "") {
                  nativeOnChange(undefined);
                } else {
                  const num = parseFloat(val);
                  if (!isNaN(num)) {
                    nativeOnChange(num);
                  }
                }
              }}
              min={getInputMin(schema)}
              max={getInputMax(schema)}
              step={schema.type === "integer" ? 1 : 0.1}
              disabled={nativeDisabled}
              className={cn(
                PARAM_COMPACT_CONTROL_CLASS,
                "w-28 tabular-nums",
                "[&::-webkit-inner-spin-button]:cursor-pointer [&::-webkit-outer-spin-button]:cursor-pointer",
                nativeDisabled && "opacity-50 cursor-not-allowed"
              )}
            />
          );
        }}
      />
    </ParamWrapper>
  );
}

// ============================================
// Enum Input (Dropdown) - with CEL expression support
// ============================================

interface EnumInputProps {
  name: string;
  schema: InputDef;
  value: unknown; // Can be string or CEL expression string
  onChange: (value: unknown) => void;
  disabled?: boolean;
  className?: string;
  availableParams?: ParamSuggestion[];
  availableNodes?: NodeSuggestion[];
  hideCELToggle?: boolean;
  isChatInputContext?: boolean;
}

function EnumInput({ 
  name, 
  schema, 
  value, 
  onChange, 
  disabled = false, 
  className,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
  isChatInputContext = false,
}: EnumInputProps) {
  return (
    <ParamWrapper 
      name={name} 
      description={getInputDescription(schema)} 
      hasDefault={getInputDefault(schema) !== undefined}
      className={className}
    >
      <DynamicInput
        type="enum"
        value={value}
        onChange={onChange}
        disabled={disabled}
        availableParams={availableParams}
        availableNodes={availableNodes}
        hideCELToggle={hideCELToggle}
        renderNative={({ value: nativeValue, onChange: nativeOnChange, disabled: nativeDisabled }) => (
          <EnumDropdown
            schema={schema}
            value={nativeValue as string}
            onChange={(v) => nativeOnChange(v)}
            disabled={nativeDisabled}
            isChatInputContext={isChatInputContext}
          />
        )}
      />
    </ParamWrapper>
  );
}

// Internal dropdown component for EnumInput
function EnumDropdown({ 
  schema, 
  value, 
  onChange, 
  disabled,
  isChatInputContext = false,
}: { 
  schema: InputDef; 
  value: string; 
  onChange: (value: string) => void; 
  disabled: boolean;
  isChatInputContext?: boolean;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const options = getInputEnumValues(schema) || [];
  const currentValue = value ?? getInputDefault(schema) ?? options[0] ?? "";

  const getDisplayLabel = (option: string) => option === "" ? "Off" : option;

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

  return (
    <div ref={dropdownRef} className="relative flex-1">
      <button
        type="button"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        className={cn(
          PARAM_SELECT_CLASS,
          disabled && "opacity-50 cursor-not-allowed"
        )}
      >
        <span className="truncate">{getDisplayLabel(currentValue) || "Select..."}</span>
        <ChevronDown className="w-4 h-4 text-muted-foreground" />
      </button>

      {isOpen && (
        <div className={cn(
          PARAM_DROPDOWN_CLASS,
          getDropdownSurfaceClass(isChatInputContext)
        )}>
          <div className="max-h-48 overflow-y-auto p-1">
            {options.map((option) => (
              <button
                key={option}
                onClick={() => {
                  onChange(option);
                  setIsOpen(false);
                }}
                className={cn(
                  PARAM_DROPDOWN_ITEM_CLASS,
                  "rounded-lg",
                  currentValue === option && "bg-primary/10 text-primary"
                )}
              >
                <div className="flex items-center justify-between">
                  <span>{getDisplayLabel(option)}</span>
                  {currentValue === option && <Check className="w-4 h-4 text-primary" />}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================
// Multi-Select Enum Input - with CEL expression support
// ============================================

interface MultiEnumInputProps {
  name: string;
  schema: InputDef;
  value: unknown; // Can be string[] or CEL expression string
  onChange: (value: unknown) => void;
  disabled?: boolean;
  className?: string;
  availableParams?: ParamSuggestion[];
  availableNodes?: NodeSuggestion[];
  hideCELToggle?: boolean;
  isChatInputContext?: boolean;
}

function MultiEnumInput({ 
  name, 
  schema, 
  value, 
  onChange, 
  disabled = false, 
  className,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
  isChatInputContext = false,
}: MultiEnumInputProps) {
  return (
    <ParamWrapper 
      name={name} 
      description={getInputDescription(schema)} 
      hasDefault={getInputDefault(schema) !== undefined}
      className={className}
    >
      <DynamicInput
        type="enum"
        value={value}
        onChange={onChange}
        disabled={disabled}
        availableParams={availableParams}
        availableNodes={availableNodes}
        hideCELToggle={hideCELToggle}
        renderNative={({ value: nativeValue, onChange: nativeOnChange, disabled: nativeDisabled }) => (
          <MultiEnumDropdown
            schema={schema}
            value={nativeValue as string[]}
            onChange={(v) => nativeOnChange(v)}
            disabled={nativeDisabled}
            isChatInputContext={isChatInputContext}
          />
        )}
      />
    </ParamWrapper>
  );
}

// Internal dropdown component for MultiEnumInput
function MultiEnumDropdown({ 
  schema, 
  value, 
  onChange, 
  disabled,
  isChatInputContext = false,
}: { 
  schema: InputDef; 
  value: string[]; 
  onChange: (value: string[]) => void; 
  disabled: boolean;
  isChatInputContext?: boolean;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const options = getInputEnumValues(schema) || [];

  const selectedValues: string[] = Array.isArray(value)
    ? value
    : (Array.isArray(getInputDefault(schema)) ? getInputDefault(schema) as string[] : []);

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

  const toggleValue = (option: string) => {
    const newSelection = selectedValues.includes(option)
      ? selectedValues.filter(v => v !== option)
      : [...selectedValues, option];
    onChange(newSelection);
  };

  const selectAll = () => onChange([...options]);
  const clearAll = () => onChange([]);

  const summary = selectedValues.length === 0
    ? "None selected"
    : options.length === 0
    ? `${selectedValues.length} selected`
    : selectedValues.length === options.length
    ? "All selected"
    : `${selectedValues.length} of ${options.length} selected`;

  return (
    <div ref={dropdownRef} className="relative flex-1">
      <button
        type="button"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        className={cn(
          PARAM_SELECT_CLASS,
          selectedValues.length === 0 && "text-muted-foreground",
          disabled && "opacity-50 cursor-not-allowed"
        )}
      >
        <span className="truncate">{summary}</span>
        <ChevronDown className="w-4 h-4 text-muted-foreground" />
      </button>

      {isOpen && (
        <div className={cn(
          PARAM_DROPDOWN_CLASS,
          getDropdownSurfaceClass(isChatInputContext)
        )}>
          <div className="flex items-center justify-between border-b border-border/50 bg-muted/20 px-3 py-2">
            <span className="text-xs text-muted-foreground">
              {selectedValues.length} selected
            </span>
            <div className="flex gap-2">
              <button type="button" onClick={selectAll} className="text-xs text-primary hover:underline">
                All
              </button>
              <button type="button" onClick={clearAll} className="text-xs text-muted-foreground hover:text-foreground">
                Clear
              </button>
            </div>
          </div>

          <div className="max-h-48 overflow-y-auto p-1">
            {options.length === 0 && selectedValues.length > 0 ? (
              // When no options defined but values exist, show selected values as removable items
              <>
                <div className="mb-1 border-b border-border/50 px-3 py-2 text-xs text-muted-foreground">
                  No enum values defined. Showing selected values:
                </div>
                {selectedValues.map((value) => (
                  <button
                    key={value}
                    type="button"
                    onClick={() => toggleValue(value)}
                    className={cn(PARAM_DROPDOWN_ITEM_CLASS, "rounded-lg")}
                  >
                    <div className="flex items-center gap-2">
                      <div className="w-4 h-4 rounded border flex items-center justify-center flex-shrink-0 bg-primary border-primary">
                        <Check className="w-3 h-3 text-primary-foreground" />
                      </div>
                      <span>{value}</span>
                    </div>
                  </button>
                ))}
              </>
            ) : options.length === 0 ? (
              <div className="px-3 py-4 text-sm text-muted-foreground text-center">
                No options available
              </div>
            ) : (
              options.map((option) => (
                <button
                  key={option}
                  type="button"
                  onClick={() => toggleValue(option)}
                  className={cn(PARAM_DROPDOWN_ITEM_CLASS, "rounded-lg")}
                >
                  <div className="flex items-center gap-2">
                    <div className={cn(
                      "w-4 h-4 rounded border flex items-center justify-center flex-shrink-0 transition-colors",
                      selectedValues.includes(option)
                        ? "bg-primary border-primary"
                        : "border-foreground/50 bg-[hsl(var(--background))]"
                    )}>
                      {selectedValues.includes(option) && (
                        <Check className="w-3 h-3 text-primary-foreground" />
                      )}
                    </div>
                    <span>{option}</span>
                  </div>
                </button>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================
// Model Input (Model Selector Dropdown) - with CEL expression support
// ============================================

interface ModelInputProps {
  name: string;
  schema: InputDef;
  value: unknown; // Can be string or CEL expression string
  onChange: (value: unknown) => void;
  disabled?: boolean;
  className?: string;
  availableParams?: ParamSuggestion[];
  availableNodes?: NodeSuggestion[];
  hideCELToggle?: boolean;
  isChatInputContext?: boolean;
}

function ModelInput({ 
  name, 
  schema, 
  value, 
  onChange, 
  disabled = false, 
  className,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
  isChatInputContext = false,
}: ModelInputProps) {
  return (
    <ParamWrapper 
      name={name} 
      description={getInputDescription(schema)} 
      hasDefault={hasDefaultValue(schema)}
      className={className}
    >
      <DynamicInput
        type="model"
        value={value}
        onChange={onChange}
        disabled={disabled}
        availableParams={availableParams}
        availableNodes={availableNodes}
        hideCELToggle={hideCELToggle}
        renderNative={({ value: nativeValue, onChange: nativeOnChange, disabled: nativeDisabled }) => (
          <ModelDropdown
            schema={schema}
            value={nativeValue as ModelSelectorValue}
            onChange={(v) => nativeOnChange(v)}
            disabled={nativeDisabled}
            isChatInputContext={isChatInputContext}
          />
        )}
      />
    </ParamWrapper>
  );
}

// ModelSelector value type - matches backend v3 format
type ModelSelectorValue = { id: string } | { tags: string[] };

// Extract model ID from ModelSelector value
function extractModelId(value: ModelSelectorValue | undefined | null): string {
  if (!value) return '';
  if ('id' in value) return value.id;
  if ('tags' in value && value.tags.length > 0) return ''; // Tags don't map to a specific ID
  return '';
}

// Internal dropdown component for ModelInput
function ModelDropdown({
  schema,
  value,
  onChange,
  disabled,
  isChatInputContext = false,
}: {
  schema: InputDef;
  value: ModelSelectorValue; // { id } or { tags }
  onChange: (value: { id: string }) => void; // Always outputs { id: string } format
  disabled: boolean;
  isChatInputContext?: boolean;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const { models, loading: isLoading, error: modelsError } = useModels();
  const isInitialized = useGlobalDataStore((state) => state.isInitialized);
  const isPrefetching = useGlobalDataStore((state) => state.isPrefetching);
  const refetchModels = useGlobalDataStore((state) => state.refetchModels);
  const setSettingsMode = useViewerStore((state) => state.setSettingsMode);

  const isActuallyLoading = isLoading || isPrefetching || !isInitialized;

  // Listen for API key updates to automatically refetch models
  useEvent("api-key:saved", async () => {
    logger.info('[ModelInput] API key saved event detected, refetching models immediately');
    try {
      await refetchModels();
      logger.info('[ModelInput] Models refetched successfully after API key save');
      setTimeout(async () => {
        const currentModels = useGlobalDataStore.getState().models;
        if (currentModels.length === 0) {
          logger.info('[ModelInput] Models still empty after delay, refetching again');
          await refetchModels();
        }
      }, 500);
    } catch (error) {
      logger.error('[ModelInput] Failed to refetch models after API key save:', error);
    }
  });

  // Extract model ID from value (handles legacy string and new { id } format)
  const valueModelId = extractModelId(value);
  const defaultModelId = extractModelId(getInputDefault(schema) as ModelSelectorValue);
  const currentValue = valueModelId || defaultModelId || "";

  const selectedModel = models.find(m => {
    if (m.id === currentValue) return true;
    const modelBaseId = m.id.split('@')[0];
    return modelBaseId === currentValue;
  });

  // Auto-select a valid model if the selected one isn't available
  useEffect(() => {
    if (isActuallyLoading || models.length === 0 || selectedModel) return;
    // Don't auto-select when value is a template/CEL expression or empty (uses default)
    if (!currentValue || (typeof currentValue === "string" && currentValue.includes("{{"))) return;

    const baseModelFamily = typeof currentValue === "string" ? currentValue.split("-")[0] : "";
    const sameFamilyModels = models.filter((m) => m.id.split("@")[0].startsWith(baseModelFamily));
    const sortedModels = sameFamilyModels.length > 0 ? sameFamilyModels : models;
    
    sortedModels.sort((a, b) => {
      const aBaseId = a.id.split("@")[0];
      const bBaseId = b.id.split("@")[0];
      const aIsPro = aBaseId.includes("pro");
      const bIsPro = bBaseId.includes("pro");
      if (aIsPro !== bIsPro) return aIsPro ? 1 : -1;
      const aVersion = aBaseId.match(/(\d+\.?\d*)/)?.[1] || "0";
      const bVersion = bBaseId.match(/(\d+\.?\d*)/)?.[1] || "0";
      if (aVersion !== bVersion) return parseFloat(bVersion) - parseFloat(aVersion);
      return 0;
    });

    const fallbackModel = sortedModels[0] || models[0];
    console.log("[ModelInput] Selected model not available, auto-selecting:", {
      oldModel: currentValue,
      newModel: fallbackModel.id,
      availableModels: models.map((m) => m.id),
    });
    onChange({ id: fallbackModel.id });
  }, [isActuallyLoading, models, selectedModel, currentValue, onChange]);

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

  if (modelsError && !isLoading) {
    console.error('[ModelInput] Error loading models:', modelsError);
  }

  return (
    <div ref={dropdownRef} className="relative flex-1">
      <button
        type="button"
        onClick={() => {
          if (!disabled && !isActuallyLoading) setIsOpen(!isOpen);
        }}
        disabled={disabled || isActuallyLoading}
        className={cn(
          PARAM_SELECT_CLASS,
          (disabled || isActuallyLoading) && "opacity-50 cursor-not-allowed"
        )}
      >
        <span className="truncate">
          {isActuallyLoading ? "Loading models..." : (selectedModel?.name || "Select model...")}
        </span>
        {isActuallyLoading ? (
          <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
        ) : (
          <ChevronDown className="w-4 h-4 text-muted-foreground" />
        )}
      </button>

      {isOpen && (
        <div className={cn(
          PARAM_DROPDOWN_CLASS,
          getDropdownSurfaceClass(isChatInputContext)
        )}>
          <div className="max-h-64 overflow-y-auto p-1">
            {models.map((model) => (
              <button
                key={model.id}
                onClick={() => {
                  onChange({ id: model.id });
                  setIsOpen(false);
                }}
                className={cn(
                  PARAM_DROPDOWN_ITEM_CLASS,
                  "rounded-lg",
                  (currentValue === model.id || model.id.split('@')[0] === currentValue) && "bg-primary/10 text-primary"
                )}
              >
                <div className="flex items-center justify-between">
                  <div>
                    <div className="font-medium">{model.name}</div>
                    <div className="text-xs text-muted-foreground">{model.provider}</div>
                  </div>
                  {(currentValue === model.id || model.id.split('@')[0] === currentValue) && <Check className="w-4 h-4 text-primary" />}
                </div>
              </button>
            ))}
            {models.length === 0 && !isActuallyLoading && (
              <div className="px-3 py-4 text-sm text-muted-foreground text-center">
                <div>
                  No models available.{" "}
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      setSettingsMode(true, "general");
                    }}
                    className="text-primary hover:underline text-xs"
                  >
                    Add API key
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================
// String Input (Text)
// ============================================

interface StringInputProps {
  name: string;
  schema: InputDef;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  className?: string;
}

function StringInput({ name, schema, value, onChange, disabled, className }: StringInputProps) {
  // Use value if provided, otherwise empty (show default as placeholder)
  const currentValue = value ?? "";
  const defaultStr = getInputDefault(schema) !== undefined ? formatValueForDisplay(getInputDefault(schema)) : undefined;
  const hasTemplate = typeof currentValue === "string" && currentValue.includes("{{");

  const isMultiline = name.toLowerCase().includes("prompt") ||
                      name.toLowerCase().includes("description") ||
                      (typeof currentValue === "string" && currentValue.includes("\n")) ||
                      (typeof defaultStr === "string" && defaultStr.includes("\n"));

  // Generate placeholder - show default if available
  const placeholder = defaultStr
    ? `${defaultStr}`
    : `Enter value...`;

  if (isMultiline) {
    return (
      <ParamWrapper
        name={name}
        description={getInputDescription(schema)}
        hasDefault={!!getInputDefault(schema)}
        defaultValue={!currentValue && defaultStr ? defaultStr : undefined}
        className={className}
      >
        <textarea
          value={currentValue}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          rows={3}
          className={cn(
            PARAM_CONTROL_CLASS,
            "min-h-24 resize-y leading-relaxed",
            hasTemplate && "border-primary/50 bg-primary/5",
            disabled && "opacity-50 cursor-not-allowed"
          )}
          placeholder={placeholder}
        />
      </ParamWrapper>
    );
  }

  return (
    <ParamWrapper
      name={name}
      description={getInputDescription(schema)}
      hasDefault={!!getInputDefault(schema)}
      defaultValue={!currentValue && defaultStr ? defaultStr : undefined}
      className={className}
    >
      <input
        type="text"
        value={currentValue}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        className={cn(
          PARAM_CONTROL_CLASS,
          hasTemplate && "border-primary/50 bg-primary/5",
          disabled && "opacity-50 cursor-not-allowed"
        )}
        placeholder={placeholder}
      />
    </ParamWrapper>
  );
}

// ============================================
// Wrapper Component
// ============================================

interface ParamWrapperProps {
  name: string;
  description?: string;
  hasDefault?: boolean; // If true, has a default value (user input optional)
  defaultValue?: unknown;
  className?: string;
  inline?: boolean; // when true, keep label and children on same row (for booleans)
  children: React.ReactNode;
}

function ParamWrapper({ name, description, hasDefault, defaultValue: _defaultValue, className, inline = false, children }: ParamWrapperProps) {
  // Format name for display:
  // 1. Strip group prefix (e.g., "Implementer.model" -> "model")
  // 2. Convert snake_case to Title Case
  const baseName = name.includes(".") ? name.split(".").pop()! : name;
  const displayName = baseName
    .split("_")
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");

  if (inline) {
    // Inline layout: label and input on the same row (used for booleans)
    return (
      <div className={cn("rounded-xl border border-border/40 bg-muted/20 px-3 py-2.5 shadow-sm shadow-black/5", className)}>
        <div className="flex items-center justify-between gap-3">
          <label className="flex min-w-0 items-center gap-1.5 text-sm font-medium text-foreground">
            <span className="truncate">{displayName}</span>
            {!hasDefault && <span className="text-destructive">*</span>}
            {description && (
              <Tooltip content={description} placement="top" delay={200}>
                <Info className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              </Tooltip>
            )}
          </label>
          {children}
        </div>
      </div>
    );
  }

  // Stacked layout: label above, children below (full width)
  return (
    <div className={cn("space-y-2 rounded-xl border border-border/40 bg-muted/20 p-3 shadow-sm shadow-black/5", className)}>
      <div className="flex items-center gap-2">
        <label className="flex min-w-0 items-center gap-1.5 text-sm font-medium text-foreground">
          <span className="truncate">{displayName}</span>
          {!hasDefault && <span className="text-destructive">*</span>}
          {description && (
            <Tooltip content={description} placement="top" delay={200}>
              <Info className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            </Tooltip>
          )}
        </label>
      </div>
      {children}
    </div>
  );
}

export default WorkflowParamInput;