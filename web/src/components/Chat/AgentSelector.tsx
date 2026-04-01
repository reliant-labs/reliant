import { useState, useRef, useEffect } from "react";
import { Users, ChevronDown, Check, Workflow } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { useWorkflows } from "../../store/globalDataStore";

interface AgentSelectorProps {
  // Current selection (agent name or workflow name)
  value?: string | null;
  onChange?: (value: string | null) => void;

  // UI state
  isStreaming?: boolean;
  disabled?: boolean;
  className?: string;
  compact?: boolean;
}

export function AgentSelector({
  value,
  onChange,
  isStreaming = false,
  disabled = false,
  className = "",
  compact = false,
}: AgentSelectorProps) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Use cached workflows from global store
  const { workflows, loading: workflowsLoading } = useWorkflows();

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

  // Check if selected value is a workflow or the default "general" agent
  const selectedWorkflow = workflows.find((wf) => wf.name === value);
  const isGeneralAgent = value === "general" || !value;

  // Get display text
  const getDisplayText = () => {
    if (!value || value === "general") return "General";
    if (selectedWorkflow) return selectedWorkflow.name;
    return value; // Fallback
  };

  const handleSelect = (newValue: string | null) => {
    onChange?.(newValue);
    setIsOpen(false);
  };

  const canInteract = !isStreaming && !disabled;

  // Dynamic tooltip showing current selection
  const getTooltipContent = () => {
    if (!value || value === "general") return "Agent: General (default)";
    if (selectedWorkflow) return `Workflow: ${selectedWorkflow.name}`;
    return `Agent: ${value}`;
  };

  return (
    <div className={`relative ${className}`} ref={dropdownRef} data-dropdown-open={isOpen}>
      {/* Selector Button */}
      <Tooltip content={getTooltipContent()} placement="top">
        <button
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center gap-1.5 rounded transition-colors text-[10px] font-medium h-6",
            canInteract
              ? "cursor-pointer hover:bg-[var(--chat-button-hover)]"
              : "cursor-default opacity-60",
            isStreaming
              ? "bg-[var(--chat-button-bg-streaming)] text-[var(--chat-button-text-streaming)]"
              : "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)]",
            compact ? "px-1.5 gap-0.5" : "px-2 gap-1"
          )}
        >
        {workflowsLoading ? (
          <>
            <div className="animate-spin rounded-full h-3 w-3 border-b-2 border-current" />
            {!compact && <span className="truncate max-w-32">Loading...</span>}
          </>
        ) : (
          <>
            {selectedWorkflow ? (
              <Workflow className={compact ? "w-3 h-3 flex-shrink-0" : "w-3 h-3 flex-shrink-0"} />
            ) : (
              <Users className={compact ? "w-3 h-3 flex-shrink-0" : "w-3 h-3 flex-shrink-0"} />
            )}
            {!compact && <span className="truncate max-w-32">{getDisplayText()}</span>}
            {canInteract && !compact && (
              <ChevronDown className="w-3 h-3 opacity-50 flex-shrink-0" />
            )}
          </>
        )}
        </button>
      </Tooltip>

      {/* Dropdown */}
      {isOpen && canInteract && (
        <div
          className="absolute left-0 rounded-md z-[1000] bottom-full mb-1 border border-border/50 min-w-64 max-w-80 shadow-lg bg-[var(--chat-dropdown-bg)]"
        >
          <div className="py-1 max-h-96 overflow-y-auto rounded-md">
              {/* General Agent Option (default) */}
              <div
                className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground bg-muted/30"
              >
                Agent
              </div>
              <button
                onClick={() => handleSelect("general")}
                className={cn(
                  "w-full px-3 py-2 text-left text-xs transition-colors border-b border-border/30 text-foreground",
                  isGeneralAgent ? "bg-accent font-semibold" : "hover:bg-accent/50"
                )}
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Users className="w-3 h-3 opacity-50 flex-shrink-0" />
                    <span>General</span>
                    <span className="text-[9px] text-primary opacity-70">(default)</span>
                  </div>
                  {isGeneralAgent && <Check className="w-3 h-3 text-primary flex-shrink-0" />}
                </div>
              </button>

              {/* Workflows Section - only show usable workflows (valid and not hidden) */}
              {workflows.filter((w) => w.is_valid && !w.is_hidden).length > 0 && (
                <>
                  <div
                    className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground border-t border-border/30 bg-muted/30"
                  >
                    Workflows
                  </div>
                  {workflows.filter((w) => w.is_valid && !w.is_hidden).map((workflow) => {
                    const isSelected = value === workflow.name;
                    return (
                      <button
                        key={workflow.name}
                        onClick={() => handleSelect(workflow.name)}
                        className={cn(
                          "w-full px-3 py-2 text-left text-xs transition-colors border-b border-border/30 text-foreground",
                          isSelected ? "bg-accent font-semibold" : "hover:bg-accent/50"
                        )}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="flex-1">
                            <div className="flex items-center gap-2 mb-0.5">
                              <Workflow className="w-3 h-3 opacity-50 flex-shrink-0" />
                              <span className="font-semibold">{workflow.name}</span>
                            </div>
                            {workflow.description && typeof workflow.description === 'string' && (
                              <div className="text-[10px] opacity-70 ml-5">
                                {workflow.description}
                              </div>
                            )}
                            <div className="text-[10px] opacity-70 ml-5">
                              {workflow.step_count} steps
                            </div>
                          </div>
                          {isSelected && (
                            <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                          )}
                        </div>
                      </button>
                    );
                  })}
                </>
              )}

              {/* Empty State */}
              {workflows.length === 0 && (
                <div className="px-3 py-4 text-xs opacity-70 text-center">
                  No workflows available
                </div>
              )}
          </div>
        </div>
      )}
    </div>
  );
}
