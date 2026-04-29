// Copyright (c) 2025 Reliant Labs
// DynamicInput - Wrapper that enables CEL expression mode for non-string inputs

import { useState, useRef, useEffect } from "react";
import type { ReactNode } from "react";
import { Code, ChevronDown } from "lucide-react";
import { cn } from "../../lib/utils";
import { formatValueForDisplay } from "../../lib/paramUtils";

export interface DynamicInputProps {
  /** The native input type for display purposes */
  type: "boolean" | "number" | "enum" | "model";
  /** Current value - can be native type or CEL expression string */
  value: unknown;
  /** Called when value changes */
  onChange: (value: unknown) => void;
  /** Whether the input is disabled */
  disabled?: boolean;
  /** Render the native input (receives value and onChange for native type) */
  renderNative: (props: {
    value: unknown;
    onChange: (value: unknown) => void;
    disabled: boolean;
  }) => ReactNode;
  /** Available workflow params for suggestions */
  availableParams?: ParamSuggestion[];
  /** Available node outputs for suggestions */
  availableNodes?: NodeSuggestion[];
  /** Hide the CEL toggle entirely (for contexts where CEL doesn't apply, like chat params) */
  hideCELToggle?: boolean;
}

export interface ParamSuggestion {
  name: string;
  type: string;
  description?: string;
}

export interface NodeSuggestion {
  id: string;
  type: string;
  outputs?: string[];
}

/**
 * Detects if a value is a CEL expression (string with {{ }} or pure expression patterns)
 */
export function isCELExpression(value: unknown): boolean {
  if (typeof value !== "string") return false;
  // Check for template syntax
  if (value.includes("{{")) return true;
  // Check for common CEL patterns (pure expressions)
  if (value.startsWith("inputs.")) return true;
  if (value.startsWith("nodes.")) return true;
  if (value.startsWith("event.")) return true;
  if (value.startsWith("outputs.")) return true;
  return false;
}

/** Converts a native value to a string for CEL mode (uses shared display formatting + proto unwrap). */
function nativeToString(value: unknown, _type: string): string {
  return formatValueForDisplay(value);
}

/**
 * DynamicInput - Wraps non-string inputs with CEL expression toggle
 *
 * When the user toggles to CEL mode, shows a text input instead of the native control.
 * Values that look like CEL expressions automatically start in CEL mode.
 */
export function DynamicInput({
  type,
  value,
  onChange,
  disabled = false,
  renderNative,
  availableParams = [],
  availableNodes = [],
  hideCELToggle = false,
}: DynamicInputProps) {
  // When disabled (read-only), hide the toggle entirely but show the correct display
  const shouldHideToggle = disabled;
  // Detect if value is already a CEL expression
  const valueIsCEL = isCELExpression(value);
  
  // Hooks must be called before any conditional returns
  const [isCELMode, setIsCELMode] = useState(valueIsCEL);
  const [showSuggestions, setShowSuggestions] = useState(false);
  const suggestionsRef = useRef<HTMLDivElement>(null);

  // Sync mode with value when it changes externally
  useEffect(() => {
    if (valueIsCEL && !isCELMode) {
      setIsCELMode(true);
    }
  }, [valueIsCEL, isCELMode]);

  // Close suggestions when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        suggestionsRef.current &&
        !suggestionsRef.current.contains(event.target as Node)
      ) {
        setShowSuggestions(false);
      }
    };

    if (showSuggestions) {
      document.addEventListener("mousedown", handleClickOutside);
      return () =>
        document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [showSuggestions]);

  // If CEL toggle is hidden, just render native input directly
  if (hideCELToggle) {
    return (
      <div className="flex-1">
        {renderNative({ value, onChange, disabled })}
      </div>
    );
  }

  // Handle toggle between modes
  const handleToggle = () => {
    if (disabled) return;

    if (isCELMode) {
      // Switching to native mode - try to parse the value
      // For now, just clear and let user re-enter (safer than guessing)
      setIsCELMode(false);
      // Keep the value as-is if it's already the right type
      // Only clear if it was a CEL expression
      if (valueIsCEL) {
        // Reset to undefined to let the native input use its default
        onChange(undefined);
      }
    } else {
      // Switching to CEL mode - convert value to string
      setIsCELMode(true);
      const strValue = nativeToString(value, type);
      // If there's a value, wrap it as a placeholder (user can edit)
      // If no value, start empty for user to type expression
      if (strValue && strValue !== "undefined") {
        // Don't auto-wrap in {{ }} - let user decide what expression to use
        onChange(strValue);
      }
    }
  };

  // Handle CEL input change
  const handleCELChange = (newValue: string) => {
    onChange(newValue);
  };

  // Insert a suggestion
  const insertSuggestion = (expr: string) => {
    onChange(expr);
    setShowSuggestions(false);
  };

  // Build suggestions list
  const suggestions: { label: string; value: string; category: string }[] = [];

  // Add workflow input params
  availableParams.forEach((param) => {
    suggestions.push({
      label: `inputs.${param.name}`,
      value: `inputs.${param.name}`,
      category: "Inputs",
    });
  });

  // Add node outputs
  availableNodes.forEach((node) => {
    suggestions.push({
      label: `nodes.${node.id}.output`,
      value: `nodes.${node.id}.output`,
      category: "Nodes",
    });
    // Add common node properties
    if (node.type === "action") {
      suggestions.push({
        label: `nodes.${node.id}.response_text`,
        value: `nodes.${node.id}.response_text`,
        category: "Nodes",
      });
    }
  });

  // Add common expressions based on type
  if (type === "boolean") {
    suggestions.push(
      {
        label: "event.succeeded",
        value: "event.succeeded",
        category: "Common",
      },
      { label: "event.failed", value: "event.failed", category: "Common" },
    );
  }

  // Group suggestions by category
  const groupedSuggestions = suggestions.reduce(
    (acc, s) => {
      if (!acc[s.category]) acc[s.category] = [];
      acc[s.category].push(s);
      return acc;
    },
    {} as Record<string, typeof suggestions>,
  );

  // CEL mode - render text input with suggestions
  if (isCELMode) {
    const celValue =
      typeof value === "string" ? value : nativeToString(value, type);

    return (
      <div className="flex items-center gap-2 flex-1">
        <div className="relative flex-1" ref={suggestionsRef}>
          <input
            type="text"
            value={celValue}
            onChange={(e) => handleCELChange(e.target.value)}
            disabled={disabled}
            placeholder="inputs.param_name"
            className={cn(
              "w-full px-3 py-1.5 text-sm rounded-md border",
              "bg-background focus:outline-none focus:ring-2 focus:ring-ring",
              "border-input",
              disabled && "opacity-50 cursor-not-allowed",
            )}
          />

          {/* Suggestions dropdown trigger */}
          {suggestions.length > 0 && !disabled && (
            <button
              type="button"
              onClick={() => setShowSuggestions(!showSuggestions)}
              className={cn(
                "absolute right-1 top-1/2 -translate-y-1/2 p-1 rounded",
                "text-muted-foreground hover:text-foreground hover:bg-muted/50",
                "transition-colors",
              )}
              title="Insert parameter"
            >
              <ChevronDown className="w-4 h-4" />
            </button>
          )}

          {/* Suggestions dropdown */}
          {showSuggestions && suggestions.length > 0 && (
            <div className="absolute top-full left-0 right-0 mt-1 z-[1000] rounded-md border border-border bg-[var(--chat-dropdown-bg)] shadow-lg max-h-48 overflow-y-auto">
              {Object.entries(groupedSuggestions).map(([category, items]) => (
                <div key={category}>
                  <div className="px-3 py-1.5 text-xs font-medium text-muted-foreground bg-muted/30 border-b border-border">
                    {category}
                  </div>
                  {items.map((item) => (
                    <button
                      key={item.value}
                      type="button"
                      onClick={() => insertSuggestion(item.value)}
                      className="w-full px-3 py-1.5 text-left text-sm hover:bg-accent/50 transition-colors"
                    >
                      {item.label}
                    </button>
                  ))}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* CEL mode toggle (active) - hidden when disabled */}
        {!shouldHideToggle && (
          <button
            type="button"
            onClick={handleToggle}
            title="Switch to literal value"
            className={cn(
              "p-1.5 rounded transition-colors flex-shrink-0",
              "bg-amber-100 dark:bg-amber-900/40 text-amber-700 dark:text-amber-400",
              "hover:bg-amber-200 dark:hover:bg-amber-800/50",
            )}
          >
            <Code className="w-4 h-4" />
          </button>
        )}
      </div>
    );
  }

  // Native mode - render the native input with toggle button
  return (
    <div className="flex items-center gap-2 flex-1">
      <div className="flex-1">
        {renderNative({
          value,
          onChange,
          disabled,
        })}
      </div>

      {/* CEL mode toggle (inactive) - hidden when disabled */}
      {!shouldHideToggle && (
        <button
          type="button"
          onClick={handleToggle}
          title="Switch to dynamic expression"
          className={cn(
            "p-1.5 rounded transition-colors flex-shrink-0",
            "text-muted-foreground hover:text-foreground hover:bg-muted/50",
          )}
        >
          <Code className="w-4 h-4" />
        </button>
      )}
    </div>
  );
}