// Copyright (c) 2025 Reliant Labs

import { useState, useEffect, useRef } from "react";
import { AlertTriangle, AlertCircle, X, ChevronDown, ChevronRight } from "lucide-react";
import { settingsGrpc, type ConfigHealth } from "../../api/settings-grpc";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { logger } from "../../lib/logger";
import { subscribeToRefetch } from "../../store/refetchStore";
import { ConfigSeverity } from "../../gen/reliant/v1/settings_pb";

interface ConfigHealthIndicatorProps {
  className?: string;
}

/**
 * Shows a notification icon when there are configuration errors or warnings.
 * Clicking shows a dropdown with the error details.
 */
export function ConfigHealthIndicator({ className }: ConfigHealthIndicatorProps) {
  const [health, setHealth] = useState<ConfigHealth | null>(null);
  const [isOpen, setIsOpen] = useState(false);
  const [expandedErrors, setExpandedErrors] = useState<Set<number>>(new Set());
  const dropdownRef = useRef<HTMLDivElement>(null);
  const projectId = useProjectStore((state) => state.currentProject?.id);

  // Fetch config health on mount and when project changes
  useEffect(() => {
    let cancelled = false;

    const fetchHealth = async () => {
      try {
        const result = await settingsGrpc.getConfigHealth(projectId);
        if (!cancelled) {
          setHealth(result);
        }
      } catch (error) {
        logger.error("[ConfigHealthIndicator] Failed to fetch config health", { error });
        if (!cancelled) {
          setHealth(null);
        }
      }
    };

    fetchHealth();

    // Subscribe to refetch events instead of polling
    const unsubscribe = subscribeToRefetch("config_health", fetchHealth);

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [projectId]);

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

  // Don't render if no errors
  if (!health || (health.error_count === 0 && health.warning_count === 0)) {
    return null;
  }

  const hasErrors = health.error_count > 0;
  const totalCount = health.error_count + health.warning_count;

  const toggleExpanded = (index: number) => {
    setExpandedErrors((prev) => {
      const next = new Set(prev);
      if (next.has(index)) {
        next.delete(index);
      } else {
        next.add(index);
      }
      return next;
    });
  };

  const getTypeLabel = (type: string): string => {
    switch (type) {
      case "preset":
        return "Preset";
      case "workflow":
        return "Workflow";
      case "mcp":
        return "MCP Server";
      case "config":
        return "Configuration";
      default:
        return type;
    }
  };

  const getTypeColor = (type: string): string => {
    switch (type) {
      case "preset":
        return "text-purple-500";
      case "workflow":
        return "text-blue-500";
      case "mcp":
        return "text-green-500";
      default:
        return "text-gray-500";
    }
  };

  return (
    <div ref={dropdownRef} className={cn("relative", className)}>
      <Tooltip
        content={`${health.error_count} error${health.error_count !== 1 ? "s" : ""}, ${health.warning_count} warning${health.warning_count !== 1 ? "s" : ""}`}
        placement="bottom"
        delay={300}
      >
        <button
          onClick={() => setIsOpen(!isOpen)}
          className={cn(
            "relative p-1.5 rounded text-xs transition-colors",
            hasErrors
              ? "text-destructive hover:bg-destructive/10"
              : "text-yellow-500 hover:bg-yellow-500/10"
          )}
          style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
          aria-label="Configuration issues"
        >
          {hasErrors ? (
            <AlertCircle className="w-4 h-4" />
          ) : (
            <AlertTriangle className="w-4 h-4" />
          )}
          {/* Badge showing count */}
          <span
            className={cn(
              "absolute -top-0.5 -right-0.5 flex items-center justify-center",
              "min-w-[14px] h-[14px] px-0.5 text-[9px] font-bold rounded-full",
              hasErrors
                ? "bg-destructive text-destructive-foreground"
                : "bg-yellow-500 text-white"
            )}
          >
            {totalCount > 99 ? "99+" : totalCount}
          </span>
        </button>
      </Tooltip>

      {/* Dropdown */}
      {isOpen && (
        <div
          className={cn(
            "absolute top-full right-0 mt-1 w-96 max-h-[400px] overflow-y-auto",
            "rounded-lg border border-border bg-popover shadow-lg z-50"
          )}
          style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
        >
          {/* Header */}
          <div className="flex items-center justify-between px-3 py-2 border-b border-border bg-muted/30">
            <div className="flex items-center gap-2">
              {hasErrors ? (
                <AlertCircle className="w-4 h-4 text-destructive" />
              ) : (
                <AlertTriangle className="w-4 h-4 text-yellow-500" />
              )}
              <span className="text-sm font-medium">Configuration Issues</span>
            </div>
            <button
              onClick={() => setIsOpen(false)}
              className="p-1 hover:bg-accent rounded transition-colors"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>

          {/* Summary */}
          <div className="px-3 py-2 text-xs text-muted-foreground border-b border-border/50">
            {health.error_count > 0 && (
              <span className="text-destructive font-medium">
                {health.error_count} error{health.error_count !== 1 ? "s" : ""}
              </span>
            )}
            {health.error_count > 0 && health.warning_count > 0 && ", "}
            {health.warning_count > 0 && (
              <span className="text-yellow-600 dark:text-yellow-500 font-medium">
                {health.warning_count} warning{health.warning_count !== 1 ? "s" : ""}
              </span>
            )}
          </div>

          {/* Error list */}
          <div className="divide-y divide-border/50">
            {health.errors.map((error, index) => (
              <div key={index} className="px-3 py-2">
                <button
                  onClick={() => toggleExpanded(index)}
                  className="w-full flex items-start gap-2 text-left"
                >
                  <span className="mt-0.5">
                    {expandedErrors.has(index) ? (
                      <ChevronDown className="w-3 h-3 text-muted-foreground" />
                    ) : (
                      <ChevronRight className="w-3 h-3 text-muted-foreground" />
                    )}
                  </span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      {error.severity === ConfigSeverity.ERROR ? (
                        <AlertCircle className="w-3.5 h-3.5 text-destructive flex-shrink-0" />
                      ) : (
                        <AlertTriangle className="w-3.5 h-3.5 text-yellow-500 flex-shrink-0" />
                      )}
                      <span className={cn("text-[10px] font-medium uppercase", getTypeColor(error.type))}>
                        {getTypeLabel(error.type)}
                      </span>
                      <span className="text-xs text-muted-foreground truncate">
                        {error.source}
                      </span>
                    </div>
                    <p className="text-xs mt-1 text-foreground/90 line-clamp-2">
                      {error.message}
                    </p>
                  </div>
                </button>

                {/* Expanded details */}
                {expandedErrors.has(index) && Object.keys(error.details).length > 0 && (
                  <div className="mt-2 ml-5 p-2 rounded bg-muted/50 text-xs">
                    <div className="font-medium text-muted-foreground mb-1">Details:</div>
                    <dl className="space-y-1">
                      {Object.entries(error.details).map(([key, value]) => (
                        <div key={key} className="flex gap-2">
                          <dt className="text-muted-foreground">{key}:</dt>
                          <dd className="text-foreground break-all">{value}</dd>
                        </div>
                      ))}
                    </dl>
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
