import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { CELInput } from "../CELInput";
import type { Step } from "../../../types/workflow";
import { FieldInput, FieldLabel, FieldSelect, SectionFields } from "./primitives";

export function getConditionExpression(condition: Step["condition"]): string {
  return condition?.expr ?? "";
}

export function getJoinCondition(condition: Step["condition"]): "all" | "any" {
  const normalized = getConditionExpression(condition);
  return normalized === "any" ? "any" : "all";
}

export function getStringValue(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }
  if (!value || typeof value !== "object") {
    return "";
  }

  const recordValue = value as {
    expr?: unknown;
    value?: unknown;
    literal?: unknown;
  };

  if (typeof recordValue.expr === "string") {
    return recordValue.expr;
  }

  if (typeof recordValue.literal === "string") {
    return recordValue.literal;
  }

  if (recordValue.value && typeof recordValue.value === "object") {
    const oneofValue = recordValue.value as { value?: unknown };
    if (typeof oneofValue.value === "string") {
      return oneofValue.value;
    }
  }

  return "";
}

// ============================================================================
// TIMEOUT SELECTOR
// ============================================================================

const TIMEOUT_OPTIONS = [
  { value: "", label: "No timeout" },
  { value: "5m", label: "5 minutes" },
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "4h", label: "4 hours" },
  { value: "24h", label: "24 hours" },
  { value: "custom", label: "Custom..." },
];

export function TimeoutSelector({
  value,
  onChange,
  disabled = false,
}: {
  value: string | undefined;
  onChange: (value: string | undefined) => void;
  disabled?: boolean;
}) {
  // Check if current value is a preset or custom
  const isPreset = !value || TIMEOUT_OPTIONS.some((opt) => opt.value === value);
  const [showCustom, setShowCustom] = useState(!isPreset);
  const [customValue, setCustomValue] = useState(!isPreset ? value : "");

  const handleSelectChange = (selected: string) => {
    if (selected === "custom") {
      setShowCustom(true);
      // Keep current custom value if any
      if (customValue) {
        onChange(customValue);
      }
    } else {
      setShowCustom(false);
      onChange(selected || undefined);
    }
  };

  const handleCustomChange = (val: string) => {
    setCustomValue(val);
    onChange(val || undefined);
  };

  return (
    <div>
      <FieldLabel>Timeout (optional)</FieldLabel>
      <SectionFields className="gap-2">
        <FieldSelect
          value={showCustom ? "custom" : value || ""}
          onChange={(e) => handleSelectChange(e.target.value)}
          disabled={disabled}
        >
          {TIMEOUT_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </FieldSelect>

        {showCustom && (
          <FieldInput
            type="text"
            value={customValue || ""}
            onChange={(e) => handleCustomChange(e.target.value)}
            placeholder="e.g., 2h30m, 45m"
            disabled={disabled}
          />
        )}
      </SectionFields>

      <p className="cpv2-field-hint">
        Maximum time to wait before timing out
      </p>
    </div>
  );
}

// ============================================================================
// ADVANCED PROJECT SETTINGS
// ============================================================================

export function AdvancedProjectSettings({
  project,
  onUpdate,
  isReadOnly = false,
  variant = 'collapsible' as 'collapsible' | 'flat',
}: {
  project?: { path?: string };
  onUpdate: (project: { path?: string } | undefined) => void;
  isReadOnly?: boolean;
  /** 'collapsible' (default) wraps in bordered collapsible box; 'flat' renders fields directly */
  variant?: 'collapsible' | 'flat';
}) {
  const [isExpanded, setIsExpanded] = useState(!!project?.path);
  const hasConfig = !!project?.path;

  const projectContent = (
    <div>
      <CELInput
        label="Project Path"
        helpTooltip="CEL expression for working directory override. The sub-workflow will execute in this project directory."
        value={project?.path || ''}
        onChange={(value) => {
          if (!value) {
            onUpdate(undefined);
          } else {
            onUpdate({ path: value });
          }
        }}
        placeholder='{{inputs.project_path}}'
        disabled={isReadOnly}
        hideCELHint
      />
    </div>
  );

  if (variant === 'flat') {
    return projectContent;
  }

  return (
    <div>
      {/* Header - collapsible */}
      <button
        type="button"
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center gap-2 py-1.5 text-sm font-medium text-foreground hover:text-foreground/80 transition-colors"
      >
        {isExpanded ? (
          <ChevronDown className="w-4 h-4 text-muted-foreground" />
        ) : (
          <ChevronRight className="w-4 h-4 text-muted-foreground" />
        )}
        <span>Advanced</span>
        {hasConfig && (
          <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">
            configured
          </span>
        )}
      </button>

      {/* Expanded content */}
      {isExpanded && (
        <div className="pt-2 space-y-3">
          {projectContent}
        </div>
      )}
    </div>
  );
}