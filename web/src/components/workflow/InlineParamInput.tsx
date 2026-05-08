// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect } from "react";
import { ChevronDown, Check } from "lucide-react";
import { cn } from "../../lib/utils";
import { formatValueForDisplay } from "../../lib/paramUtils";
import { Tooltip } from "../ui/Tooltip";
import type { InputDef } from "../../lib/inputHelpers";
import { getInputDescription, getInputDefault, getInputEnumValues, getInputMulti, getInputMin, getInputMax } from "../../lib/inputHelpers";
import type { LucideIcon } from "lucide-react";

interface InlineParamInputProps {
  name: string;
  schema: InputDef;
  value: unknown;
  onChange: (value: unknown) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
  /** Optional icon to display instead of label prefix */
  icon?: LucideIcon;
}

/**
 * Compact inline parameter input for the chat input bottom bar.
 * Renders as a small pill-style button with dropdown or inline editing.
 */
export function InlineParamInput({
  name,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
  icon,
}: InlineParamInputProps) {
  // Format display name from snake_case
  const displayName = name
    .split("_")
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");

  switch (schema.type) {
    case "enum":
      // Check if multi-select enum
      if (getInputMulti(schema)) {
        return (
          <InlineMultiEnumInput
            name={name}
            displayName={displayName}
            schema={schema}
            value={value as string[]}
            onChange={onChange}
            disabled={disabled}
            isStreaming={isStreaming}
            className={className}
            icon={icon}
          />
        );
      }
      return (
        <InlineEnumInput
          name={name}
          displayName={displayName}
          schema={schema}
          value={value as string}
          onChange={onChange}
          disabled={disabled}
          isStreaming={isStreaming}
          className={className}
          icon={icon}
        />
      );

    case "boolean":
      return (
        <InlineBooleanInput
          name={name}
          displayName={displayName}
          schema={schema}
          value={value as boolean}
          onChange={onChange}
          disabled={disabled}
          isStreaming={isStreaming}
          className={className}
        />
      );

    case "string":
      return (
        <InlineStringInput
          name={name}
          displayName={displayName}
          schema={schema}
          value={value as string}
          onChange={onChange}
          disabled={disabled}
          isStreaming={isStreaming}
          className={className}
        />
      );

    case "number":
    case "integer":
      return (
        <InlineNumberInput
          name={name}
          displayName={displayName}
          schema={schema}
          value={value as number}
          onChange={onChange}
          disabled={disabled}
          isStreaming={isStreaming}
          className={className}
        />
      );

    default:
      // For unsupported types, show a label-only pill
      return (
        <Tooltip content={`${displayName}: ${getInputDescription(schema) || "No description"}`} placement="top">
          <div
            className={cn(
              "flex items-center gap-1 rounded-full text-[10px] font-medium h-6 px-2.5",
              "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
              "opacity-60 cursor-not-allowed",
              className
            )}
          >
            <span>{displayName}</span>
          </div>
        </Tooltip>
      );
  }
}

// ============================================
// Inline Enum Input (Dropdown)
// ============================================

interface InlineEnumInputProps {
  name: string;
  displayName: string;
  schema: InputDef;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
  icon?: LucideIcon;
}

function InlineEnumInput({
  name: _name,
  displayName,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
  icon: Icon,
}: InlineEnumInputProps) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const options = getInputEnumValues(schema) || [];
  const currentValue = value ?? getInputDefault(schema) ?? options[0] ?? "";
  const canInteract = !isStreaming && !disabled;

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

  // Format option for display
  const formatOption = (opt: unknown) => {
    if (opt === "" || opt === null || opt === undefined) return "Off";
    const str = formatValueForDisplay(opt);
    return str.charAt(0).toUpperCase() + str.slice(1);
  };

  return (
    <div ref={dropdownRef} className={cn("relative", className)}>
      <Tooltip 
        content={getInputDescription(schema) || `${displayName}: ${currentValue}`} 
        placement="top"
      >
        <button
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-1 rounded-full transition-colors text-[10px] font-medium h-6 px-2.5",
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
          {Icon ? (
            <Icon className="w-3 h-3 text-muted-foreground flex-shrink-0" />
          ) : (
            <span className="text-muted-foreground">{displayName}:</span>
          )}
          <span className="truncate max-w-20">{formatOption(currentValue)}</span>
          <ChevronDown className="w-2.5 h-2.5 opacity-50 flex-shrink-0" />
        </button>
      </Tooltip>

      {isOpen && canInteract && (
        <div
          className={cn(
            "absolute bottom-full left-0 mb-1 min-w-32 rounded-lg border border-border bg-[var(--chat-dropdown-bg)] shadow-lg z-[1000]",
            "max-h-48 overflow-y-auto"
          )}
        >
          <div className="py-1">
            {options.map((option) => {
              const isSelected = currentValue === option;
              return (
                <button
                  key={option}
                  onClick={() => {
                    onChange(option);
                    setIsOpen(false);
                  }}
                  className={cn(
                    "w-full flex items-center justify-between gap-2 px-3 py-1.5 text-left text-xs",
                    "hover:bg-accent transition-colors",
                    isSelected && "bg-accent"
                  )}
                >
                  <span>{formatOption(option)}</span>
                  {isSelected && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================
// Inline Multi-Select Enum Input (Dropdown with checkboxes)
// ============================================

interface InlineMultiEnumInputProps {
  name: string;
  displayName: string;
  schema: InputDef;
  value: string[];
  onChange: (value: string[]) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
  icon?: LucideIcon;
}

function InlineMultiEnumInput({
  name: _name,
  displayName,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
  icon: Icon,
}: InlineMultiEnumInputProps) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const options = getInputEnumValues(schema) || [];
  const canInteract = !isStreaming && !disabled;

  const selectedValues: string[] = Array.isArray(value)
    ? value
    : (Array.isArray(getInputDefault(schema)) ? getInputDefault(schema) as string[] : []);

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

  const toggleValue = (option: string) => {
    const newSelection = selectedValues.includes(option)
      ? selectedValues.filter(v => v !== option)
      : [...selectedValues, option];
    onChange(newSelection);
  };

  const selectAll = () => onChange([...options]);
  const clearAll = () => onChange([]);

  // Format option for display
  const formatOption = (opt: unknown) => {
    if (opt === "" || opt === null || opt === undefined) return "Off";
    const str = formatValueForDisplay(opt);
    return str.charAt(0).toUpperCase() + str.slice(1);
  };

  // Generate summary text
  const summary = selectedValues.length === 0
    ? "None"
    : selectedValues.length === options.length
    ? "All"
    : `${selectedValues.length}`;

  return (
    <div ref={dropdownRef} className={cn("relative", className)}>
      <Tooltip 
        content={getInputDescription(schema) || `${displayName}: ${selectedValues.length} selected`} 
        placement="top"
      >
        <button
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-1 rounded-full transition-colors text-[10px] font-medium h-6 px-2.5",
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
          {Icon ? (
            <Icon className="w-3 h-3 text-muted-foreground flex-shrink-0" />
          ) : (
            <span className="text-muted-foreground">{displayName}:</span>
          )}
          <span className="truncate max-w-20">{summary}</span>
          <ChevronDown className="w-2.5 h-2.5 opacity-50 flex-shrink-0" />
        </button>
      </Tooltip>

      {isOpen && canInteract && (
        <div
          className={cn(
            "absolute bottom-full left-0 mb-1 min-w-40 rounded-lg border border-border bg-[var(--chat-dropdown-bg)] shadow-lg z-[1000]",
            "max-h-64 overflow-y-auto"
          )}
        >
          {/* Header with select all/clear */}
          <div className="flex items-center justify-between px-3 py-2 border-b border-border sticky top-0 bg-[var(--chat-dropdown-bg)]">
            <span className="text-[10px] text-muted-foreground">
              {selectedValues.length} selected
            </span>
            <div className="flex gap-2">
              <button 
                type="button" 
                onClick={selectAll} 
                className="text-[10px] text-primary hover:underline"
              >
                All
              </button>
              <button 
                type="button" 
                onClick={clearAll} 
                className="text-[10px] text-muted-foreground hover:text-foreground"
              >
                Clear
              </button>
            </div>
          </div>

          <div className="py-1">
            {options.map((option) => {
              const isSelected = selectedValues.includes(option);
              return (
                <button
                  key={option}
                  onClick={() => toggleValue(option)}
                  className={cn(
                    "w-full flex items-center gap-2 px-3 py-1.5 text-left text-xs",
                    "hover:bg-accent transition-colors"
                  )}
                >
                  <div className={cn(
                    "w-3.5 h-3.5 rounded border flex items-center justify-center flex-shrink-0 transition-colors",
                    isSelected
                      ? "bg-primary border-primary"
                      : "border-foreground/50 bg-[hsl(var(--background))]"
                  )}>
                    {isSelected && (
                      <Check className="w-2.5 h-2.5 text-primary-foreground" />
                    )}
                  </div>
                  <span>{formatOption(option)}</span>
                </button>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

// ============================================
// Inline Boolean Input (Toggle Pill)
// ============================================

interface InlineBooleanInputProps {
  name: string;
  displayName: string;
  schema: InputDef;
  value: boolean;
  onChange: (value: boolean) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
}

function InlineBooleanInput({
  displayName,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
}: InlineBooleanInputProps) {
  const isOn = value ?? getInputDefault(schema) ?? false;
  const canInteract = !isStreaming && !disabled;

  return (
    <Tooltip 
      content={getInputDescription(schema) || `${displayName}: ${isOn ? "On" : "Off"}`} 
      placement="top"
    >
      <button
        onClick={() => canInteract && onChange(!isOn)}
        disabled={!canInteract}
        className={cn(
          "flex items-center gap-1 rounded-full transition-colors text-[10px] font-medium h-6 px-2.5",
          canInteract
            ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
            : "cursor-default opacity-60",
          isOn
            ? "bg-primary/20 text-primary"
            : isStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
          className
        )}
      >
        <span>{displayName}</span>
        <span className={cn(
          "w-3 h-3 rounded-full transition-colors",
          isOn ? "bg-primary" : "bg-muted-foreground/30"
        )} />
      </button>
    </Tooltip>
  );
}

// ============================================
// Inline String Input (Text with inline edit)
// ============================================

interface InlineStringInputProps {
  name: string;
  displayName: string;
  schema: InputDef;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
}

function InlineStringInput({
  displayName,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
}: InlineStringInputProps) {
  const [isEditing, setIsEditing] = useState(false);
  const [tempValue, setTempValue] = useState(value || "");
  const inputRef = useRef<HTMLInputElement>(null);
  const currentValue = value ?? getInputDefault(schema) ?? "";
  const canInteract = !isStreaming && !disabled;

  useEffect(() => {
    if (isEditing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [isEditing]);

  const handleBlur = () => {
    setIsEditing(false);
    onChange(tempValue);
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      setIsEditing(false);
      onChange(tempValue);
    } else if (e.key === "Escape") {
      setIsEditing(false);
      setTempValue(currentValue);
    }
  };

  if (isEditing) {
    return (
      <input
        ref={inputRef}
        type="text"
        value={tempValue}
        onChange={(e) => setTempValue(e.target.value)}
        onBlur={handleBlur}
        onKeyDown={handleKeyDown}
        className={cn(
          "h-6 px-2 text-[10px] rounded border border-primary bg-background",
          "focus:outline-none focus:ring-1 focus:ring-ring/20 focus:border-ring",
          "min-w-20 max-w-32",
          className
        )}
        placeholder={displayName}
      />
    );
  }

  return (
    <Tooltip 
      content={getInputDescription(schema) || `${displayName}: ${currentValue || "(empty)"}`} 
      placement="top"
    >
      <button
        onClick={() => canInteract && setIsEditing(true)}
        disabled={!canInteract}
        className={cn(
          "flex items-center gap-1 rounded-full transition-colors text-[10px] font-medium h-6 px-2.5",
          canInteract
            ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
            : "cursor-default opacity-60",
          isStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
          !currentValue && "border border-dashed border-warning/50",
          className
        )}
      >
        <span className="text-muted-foreground">{displayName}:</span>
        <span className={cn("truncate max-w-20", !currentValue && "text-warning")}>
          {currentValue || "required"}
        </span>
      </button>
    </Tooltip>
  );
}

// ============================================
// Inline Number Input
// ============================================

interface InlineNumberInputProps {
  name: string;
  displayName: string;
  schema: InputDef;
  value: number;
  onChange: (value: number) => void;
  disabled?: boolean;
  isStreaming?: boolean;
  className?: string;
}

function InlineNumberInput({
  displayName,
  schema,
  value,
  onChange,
  disabled = false,
  isStreaming = false,
  className = "",
}: InlineNumberInputProps) {
  const [isEditing, setIsEditing] = useState(false);
  const [tempValue, setTempValue] = useState(String(value ?? getInputDefault(schema) ?? ""));
  const inputRef = useRef<HTMLInputElement>(null);
  const currentValue = value ?? getInputDefault(schema);
  const canInteract = !isStreaming && !disabled;

  useEffect(() => {
    if (isEditing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [isEditing]);

  const handleBlur = () => {
    setIsEditing(false);
    const parsed = parseFloat(tempValue);
    if (!isNaN(parsed)) {
      let clamped = parsed;
      if (getInputMin(schema) !== undefined) clamped = Math.max(getInputMin(schema)!, clamped);
      if (getInputMax(schema) !== undefined) clamped = Math.min(getInputMax(schema)!, clamped);
      onChange(clamped);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      handleBlur();
    } else if (e.key === "Escape") {
      setIsEditing(false);
      setTempValue(String(currentValue ?? ""));
    }
  };

  if (isEditing) {
    return (
      <input
        ref={inputRef}
        type="number"
        value={tempValue}
        onChange={(e) => setTempValue(e.target.value)}
        onBlur={handleBlur}
        onKeyDown={handleKeyDown}
        min={getInputMin(schema)}
        max={getInputMax(schema)}
        step={schema.type === "integer" ? 1 : 0.1}
        className={cn(
          "h-6 px-2 text-[10px] rounded border border-primary bg-background",
          "focus:outline-none focus:ring-1 focus:ring-ring/20 focus:border-ring",
          "w-20",
          className
        )}
        placeholder={displayName}
      />
    );
  }

  const displayValue = currentValue !== undefined ? String(currentValue) : "";

  return (
    <Tooltip 
      content={getInputDescription(schema) || `${displayName}: ${displayValue || "(empty)"}`} 
      placement="top"
    >
      <button
        onClick={() => canInteract && setIsEditing(true)}
        disabled={!canInteract}
        className={cn(
          "flex items-center gap-1 rounded-full transition-colors text-[10px] font-medium h-6 px-2.5",
          canInteract
            ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
            : "cursor-default opacity-60",
          isStreaming
            ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
            : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
          currentValue === undefined && "border border-dashed border-warning/50",
          className
        )}
      >
        <span className="text-muted-foreground">{displayName}:</span>
        <span className={cn("tabular-nums", currentValue === undefined && "text-warning")}>
          {displayValue || "required"}
        </span>
      </button>
    </Tooltip>
  );
}

export default InlineParamInput;