// Copyright (c) 2025 Reliant Labs

import { Globe, FolderGit, FolderLock, HelpCircle } from "lucide-react";
import { cn } from "../../lib/utils";
import { ConfigScope } from "../../gen/reliant/v1/common_pb";
import { Tooltip } from "./Tooltip";

export interface ConfigScopeSelectorProps {
  /** Currently selected scope */
  value: ConfigScope;
  /** Callback when scope changes */
  onChange: (scope: ConfigScope) => void;
  /** Whether to show the "Remember this choice" checkbox */
  showRememberChoice?: boolean;
  /** Current state of remember checkbox */
  rememberChoice?: boolean;
  /** Callback when remember choice changes */
  onRememberChoiceChange?: (remember: boolean) => void;
  /** Optional label override */
  label?: string;
  /** Optional help text */
  helpText?: string;
  /** Disable the selector */
  disabled?: boolean;
  /** Additional className */
  className?: string;
  /** Compact mode for inline usage */
  compact?: boolean;
}

interface ScopeOption {
  value: ConfigScope;
  label: string;
  description: string;
  icon: typeof Globe;
}

const SCOPE_OPTIONS: ScopeOption[] = [
  {
    value: ConfigScope.GLOBAL,
    label: "Global",
    description: "Available in all projects",
    icon: Globe,
  },
  {
    value: ConfigScope.PROJECT,
    label: "Project",
    description: "Shared with team (committed to git)",
    icon: FolderGit,
  },
  {
    value: ConfigScope.PROJECT_LOCAL,
    label: "Project (Local)",
    description: "Personal config (gitignored)",
    icon: FolderLock,
  },
];

/**
 * Scope selector component for choosing where to save configuration.
 * Used when installing MCP servers or saving workflows.
 */
export function ConfigScopeSelector({
  value,
  onChange,
  showRememberChoice = false,
  rememberChoice = false,
  onRememberChoiceChange,
  label = "Save to",
  helpText,
  disabled = false,
  className,
  compact = false,
}: ConfigScopeSelectorProps) {
  return (
    <div className={cn("space-y-3", className)}>
      {/* Label with optional help */}
      <div className="flex items-center gap-2">
        <label className="text-sm font-medium text-foreground">{label}</label>
        {helpText && (
          <Tooltip content={helpText} placement="top">
            <HelpCircle className="w-4 h-4 text-muted-foreground cursor-help" />
          </Tooltip>
        )}
      </div>

      {/* Scope options */}
      <div className={cn("space-y-2", compact && "space-y-1")}>
        {SCOPE_OPTIONS.map((option) => {
          const Icon = option.icon;
          const isSelected = value === option.value;

          return (
            <button
              key={option.value}
              type="button"
              disabled={disabled}
              onClick={() => onChange(option.value)}
              className={cn(
                "w-full flex items-start gap-3 p-3 rounded-lg border-2 text-left transition-all",
                isSelected
                  ? "border-primary bg-primary/5"
                  : "border-border hover:border-primary/50 hover:bg-muted/50",
                disabled && "opacity-50 cursor-not-allowed",
                compact && "p-2"
              )}
            >
              {/* Radio indicator */}
              <div
                className={cn(
                  "mt-0.5 w-4 h-4 rounded-full border-2 flex items-center justify-center flex-shrink-0",
                  isSelected ? "border-primary" : "border-muted-foreground/50"
                )}
              >
                {isSelected && (
                  <div className="w-2 h-2 rounded-full bg-primary" />
                )}
              </div>

              {/* Icon */}
              <Icon
                className={cn(
                  "w-5 h-5 flex-shrink-0 mt-0.5",
                  isSelected ? "text-primary" : "text-muted-foreground"
                )}
              />

              {/* Text */}
              <div className="flex-1 min-w-0">
                <div
                  className={cn(
                    "font-medium",
                    isSelected ? "text-foreground" : "text-foreground/80",
                    compact && "text-sm"
                  )}
                >
                  {option.label}
                </div>
                {!compact && (
                  <div className="text-sm text-muted-foreground">
                    {option.description}
                  </div>
                )}
              </div>
            </button>
          );
        })}
      </div>

      {/* Remember choice checkbox */}
      {showRememberChoice && (
        <label className="flex items-center gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={rememberChoice}
            onChange={(e) => onRememberChoiceChange?.(e.target.checked)}
            disabled={disabled}
            className="w-4 h-4 rounded border-border text-primary focus:ring-primary focus:ring-offset-0"
          />
          <span className="text-sm text-muted-foreground">
            Remember this choice
          </span>
        </label>
      )}
    </div>
  );
}

/**
 * Helper to get the label for a scope value
 */
export function getScopeLabel(scope: ConfigScope): string {
  const option = SCOPE_OPTIONS.find((o) => o.value === scope);
  return option?.label ?? "Project";
}

/**
 * Helper to get the description for a scope value
 */
export function getScopeDescription(scope: ConfigScope): string {
  const option = SCOPE_OPTIONS.find((o) => o.value === scope);
  return option?.description ?? "";
}

export { ConfigScope };
