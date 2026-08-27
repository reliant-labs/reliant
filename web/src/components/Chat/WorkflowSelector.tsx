// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect, useMemo, useCallback } from "react";
import { Workflow, ChevronDown, Check, Star } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { useWorkflows } from "../../store/globalDataStore";
import { usePreferencesStore, DEFAULT_WORKFLOW } from "../../store/preferencesStore";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../workflow/useWorkflowInputs";

// Workflow refs appear in two formats: bare names from ListWorkflows ("agent")
// and URIs from starter cards / preferences ("builtin://agent"). Every ref
// comparison in this component must go through sameWorkflow so a starter-card
// selection ("builtin://forge-one-shot") matches its list entry — otherwise
// the trigger silently falls back to the default label.
const sameWorkflow = (a: string, b: string) =>
  normalizeWorkflowRef(a) === normalizeWorkflowRef(b);

interface WorkflowSelectorProps {
  // Current workflow name (null = use user's default workflow)
  value?: string | null;
  
  // Called when workflow selection changes
  onChange?: (workflowName: string | null) => void;

  // UI state
  isStreaming?: boolean;
  disabled?: boolean;
  className?: string;
  compact?: boolean;

  /**
   * Optional controlled open state, so a keyboard shortcut can open the picker.
   * Follows the same convention as the Dropdown primitive: when `isOpen` is
   * provided the parent owns the state, otherwise it stays internal.
   */
  isOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export function WorkflowSelector({
  value,
  onChange,
  isStreaming = false,
  disabled = false,
  className = "",
  compact = false,
  isOpen: controlledIsOpen,
  onOpenChange,
}: WorkflowSelectorProps) {
  const [uncontrolledIsOpen, setUncontrolledIsOpen] = useState(false);
  const isOpen = controlledIsOpen ?? uncontrolledIsOpen;
  const setIsOpen = useCallback(
    (next: boolean | ((prev: boolean) => boolean)) => {
      const resolve = (prev: boolean) =>
        typeof next === "function" ? next(prev) : next;
      if (onOpenChange) onOpenChange(resolve(isOpen));
      else setUncontrolledIsOpen(resolve);
    },
    [isOpen, onOpenChange],
  );
  const [contextMenu, setContextMenu] = useState<{ workflowName: string; x: number; y: number } | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const contextMenuRef = useRef<HTMLDivElement>(null);

  const { workflows, loading: workflowsLoading } = useWorkflows();
  const { preferences, isLoading: preferencesLoading, loadPreferences, isWorkflowHidden } = usePreferencesStore();
  const updatePreferences = usePreferencesStore((state) => state.updatePreferences);

  // Ensure preferences are loaded
  useEffect(() => {
    if (!preferences && !preferencesLoading) {
      loadPreferences();
    }
  }, [preferences, preferencesLoading, loadPreferences]);

  // Get user's default workflow from preferences, fallback to builtin://agent
  const userDefaultWorkflow = preferences?.defaultWorkflow || DEFAULT_WORKFLOW;

  // Separate workflows into builtin and custom, filter out hidden, deduplicate, and sort
  const { builtinWorkflows, customWorkflows } = useMemo(() => {
    // Deduplicate workflows by name - keep the first occurrence (user workflows come after project, so they'll be kept)
    const seen = new Set<string>();
    const unique = workflows.filter(w => {
      if (isWorkflowHidden(w.name)) return false;
      const normalizedName = w.name.toLowerCase().trim();
      if (seen.has(normalizedName)) {
        return false; // Skip duplicate
      }
      seen.add(normalizedName);
      return true;
    });

    // Separate into builtin and custom
    const builtin: typeof unique = [];
    const custom: typeof unique = [];

    unique.forEach(w => {
      const isBuiltin = w.name.startsWith("builtin://") || w.source === "builtin";
      if (isBuiltin) {
        builtin.push(w);
      } else {
        custom.push(w);
      }
    });

    // Sort builtin workflows: user's default first (if it's builtin), then alphabetically
    builtin.sort((a, b) => {
      if (sameWorkflow(a.name, userDefaultWorkflow)) return -1;
      if (sameWorkflow(b.name, userDefaultWorkflow)) return 1;
      return a.name.localeCompare(b.name);
    });

    // Sort custom workflows: user's default first (if it's custom), then alphabetically
    custom.sort((a, b) => {
      if (sameWorkflow(a.name, userDefaultWorkflow)) return -1;
      if (sameWorkflow(b.name, userDefaultWorkflow)) return 1;
      return a.name.localeCompare(b.name);
    });

    return { builtinWorkflows: builtin, customWorkflows: custom };
  }, [workflows, userDefaultWorkflow, isWorkflowHidden]);

  // Combined list for finding selected workflow
  const sortedWorkflows = useMemo(() => {
    return [...builtinWorkflows, ...customWorkflows];
  }, [builtinWorkflows, customWorkflows]);

  // Find selected workflow - null means use user's default
  const effectiveValue = value || userDefaultWorkflow;
  const selectedWorkflow = useMemo(() =>
    sortedWorkflows.find(w => sameWorkflow(w.name, effectiveValue)),
    [sortedWorkflows, effectiveValue]
  );

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
      if (contextMenuRef.current && !contextMenuRef.current.contains(event.target as Node)) {
        setContextMenu(null);
      }
    };

    if (isOpen || contextMenu) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen, contextMenu, setIsOpen]);

  const handleSelect = useCallback((workflowName: string | null) => {
    onChange?.(workflowName);
    setIsOpen(false);
  }, [onChange, setIsOpen]);

  const handleContextMenu = useCallback((e: React.MouseEvent, workflowName: string) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({ workflowName, x: e.clientX, y: e.clientY });
  }, []);

  const handleSetAsDefault = useCallback(async (workflowName: string) => {
    await updatePreferences({ defaultWorkflow: workflowName });
    setContextMenu(null);
  }, [updatePreferences]);

  const canInteract = !isStreaming && !disabled;

  // Fall back to the effective value (not the default): an unmatched
  // selection should still label the trigger with what is actually selected.
  const displayText = selectedWorkflow
    ? getWorkflowDisplayName(selectedWorkflow.name, true)
    : getWorkflowDisplayName(effectiveValue, true);

  return (
    <div ref={dropdownRef} className={cn("relative", className)}>
      <Tooltip content={`Workflow: ${displayText}`} placement="top">
        <button
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-1.5 rounded-full transition-colors text-2xs font-medium h-6",
            canInteract
              ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
              : "cursor-default opacity-60",
            isOpen
              ? "bg-primary/20 text-primary"
              : isStreaming
              ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
            compact ? "px-1.5 gap-0.5" : "px-2.5 gap-1"
          )}
        >
          {workflowsLoading ? (
            <div className="animate-spin rounded-full h-3 w-3 border-b-2 border-current" />
          ) : (
            <>
              <Workflow className="w-3 h-3 flex-shrink-0" />
              {!compact && <span className="truncate max-w-24">{displayText}</span>}
              {!compact && <ChevronDown className="w-2.5 h-2.5 ml-0.5 opacity-50" />}
            </>
          )}
        </button>
      </Tooltip>

      {/* Dropdown */}
      {isOpen && canInteract && (
        <div
          className={cn(
            "absolute bottom-full left-0 mb-1 w-56 rounded-lg border border-border bg-[var(--chat-dropdown-bg)] shadow-lg z-[1000]",
            "max-h-64 overflow-y-auto"
          )}
        >
          <div className="py-1">
            {workflowsLoading ? (
              <div className="px-3 py-2 text-xs text-muted-foreground">
                Loading...
              </div>
            ) : sortedWorkflows.length === 0 ? (
              <div className="px-3 py-2 text-xs text-muted-foreground">
                No workflows available
              </div>
            ) : (
              <>
                {/* Built-in Workflows Section */}
                {builtinWorkflows.length > 0 && (
                  <>
                    <div className="px-3 py-1.5 text-2xs font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-b border-border/30">
                      Built-in Workflows
                    </div>
                    {builtinWorkflows.map((workflow) => {
                      const isSelected = sameWorkflow(effectiveValue, workflow.name);
                      const isDefault = sameWorkflow(workflow.name, userDefaultWorkflow);
                      return (
                        <button
                          key={workflow.name}
                          onClick={() => handleSelect(
                            // If selecting user's default, pass null (use default)
                            sameWorkflow(workflow.name, userDefaultWorkflow) ? null : workflow.name
                          )}
                          onContextMenu={(e) => handleContextMenu(e, workflow.name)}
                          className={cn(
                            "w-full flex items-center gap-2 px-3 py-1.5 text-left text-xs",
                            "hover:bg-accent transition-colors",
                            isSelected && "bg-accent"
                          )}
                        >
                          <div className="flex-shrink-0 w-4 flex justify-center">
                            {isSelected ? (
                              <Check className="w-3.5 h-3.5 text-primary" />
                            ) : (
                              <Workflow className="w-3.5 h-3.5 text-muted-foreground" />
                            )}
                          </div>
                          <div className="flex-1 min-w-0">
                            <div className="font-medium truncate flex items-center gap-1">
                              {getWorkflowDisplayName(workflow.name, true)}
                              {isDefault && (
                                <Star className="w-3 h-3 text-yellow-500 fill-yellow-500" />
                              )}
                            </div>
                            {workflow.description && typeof workflow.description === 'string' && (
                              <div className="text-2xs text-muted-foreground truncate">
                                {workflow.description}
                              </div>
                            )}
                          </div>
                        </button>
                      );
                    })}
                  </>
                )}

                {/* Custom Workflows Section */}
                {customWorkflows.length > 0 && (
                  <>
                    {builtinWorkflows.length > 0 && (
                      <div className="border-t border-border/30 my-1" />
                    )}
                    <div className="px-3 py-1.5 text-2xs font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30 border-b border-border/30">
                      Custom Workflows
                    </div>
                    {customWorkflows.map((workflow) => {
                      const isSelected = sameWorkflow(effectiveValue, workflow.name);
                      const isDefault = sameWorkflow(workflow.name, userDefaultWorkflow);
                      return (
                        <button
                          key={workflow.name}
                          onClick={() => handleSelect(
                            // If selecting user's default, pass null (use default)
                            sameWorkflow(workflow.name, userDefaultWorkflow) ? null : workflow.name
                          )}
                          onContextMenu={(e) => handleContextMenu(e, workflow.name)}
                          className={cn(
                            "w-full flex items-center gap-2 px-3 py-1.5 text-left text-xs",
                            "hover:bg-accent transition-colors",
                            isSelected && "bg-accent"
                          )}
                        >
                          <div className="flex-shrink-0 w-4 flex justify-center">
                            {isSelected ? (
                              <Check className="w-3.5 h-3.5 text-primary" />
                            ) : (
                              <Workflow className="w-3.5 h-3.5 text-muted-foreground" />
                            )}
                          </div>
                          <div className="flex-1 min-w-0">
                            <div className="font-medium truncate flex items-center gap-1">
                              {getWorkflowDisplayName(workflow.name, true)}
                              {isDefault && (
                                <Star className="w-3 h-3 text-yellow-500 fill-yellow-500" />
                              )}
                            </div>
                            {workflow.description && typeof workflow.description === 'string' && (
                              <div className="text-2xs text-muted-foreground truncate">
                                {workflow.description}
                              </div>
                            )}
                          </div>
                        </button>
                      );
                    })}
                  </>
                )}
              </>
            )}
          </div>
        </div>
      )}

      {/* Context Menu */}
      {contextMenu && (
        <div
          ref={contextMenuRef}
          className="fixed z-[100] rounded-md border border-border bg-popover shadow-lg py-1 min-w-[180px]"
          style={{
            left: `${contextMenu.x}px`,
            top: `${contextMenu.y}px`,
          }}
        >
          <button
            onClick={() => handleSetAsDefault(contextMenu.workflowName)}
            className="w-full px-3 py-1.5 text-left text-xs hover:bg-accent transition-colors flex items-center gap-2"
          >
            <Star className="w-3.5 h-3.5" />
            Set as Default Workflow
          </button>
        </div>
      )}
    </div>
  );
}

export default WorkflowSelector;