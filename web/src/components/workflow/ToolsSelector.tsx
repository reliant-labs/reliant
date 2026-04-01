// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect, useMemo } from "react";
import {
  Wrench,
  ChevronDown,
  Check,
  X,
  CheckSquare,
  Square,
  MinusSquare,
  Plus,
  Tag,
  Sparkles,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { api } from "../../api/client";

interface ToolInfo {
  name: string;
  description: string;
  category: string;
}

interface ToolsSelectorProps {
  // Currently selected tool names and/or expressions
  value: string[];

  // Called when selection changes
  onChange: (tools: string[]) => void;

  // Whether the selector is disabled
  disabled?: boolean;

  // Additional class names
  className?: string;

  // Description to show
  description?: string;

  // Whether to hide the internal "Tools" label + select/clear header
  // (when ToolsSelector is embedded inside another component that provides its own label)
  hideLabel?: boolean;
}

const QUICK_EXPRESSIONS: Array<{ token: string; description: string }> = [
  {
    token: "tag:default",
    description: "Recommended built-in tool set",
  },
  {
    token: "tag:mcp",
    description: "Include all connected MCP tools",
  },
  {
    token: "tag:search",
    description: "grep / glob and other discovery tools",
  },
  {
    token: "tag:web",
    description: "fetch / websearch",
  },
  {
    token: "!tag:shell",
    description: "Remove bash/powershell tools",
  },
  {
    token: "mcp__*",
    description: "Wildcard include for MCP tool names",
  },
];

function normalizeSelection(raw: string[]): string[] {
  const seen = new Set<string>();
  const output: string[] = [];

  for (const token of Array.isArray(raw) ? raw : []) {
    if (typeof token !== "string") continue;
    const trimmed = token.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    output.push(trimmed);
  }

  return output;
}

function isLikelyExpression(token: string): boolean {
  return (
    token.startsWith("tag:") ||
    token.startsWith("!") ||
    token.includes("*") ||
    token.startsWith("spawn:")
  );
}

function isValidToolToken(token: string): boolean {
  if (!token) return false;

  if (token.startsWith("spawn:")) {
    return /^spawn:[^()\s]+\([^)]*\)$/.test(token);
  }

  if (token.startsWith("!")) {
    const remainder = token.slice(1).trim();
    return !!remainder;
  }

  if (token.startsWith("tag:")) {
    return /^tag:[a-zA-Z0-9_-]+$/.test(token);
  }

  // MCP explicit tool name or MCP wildcard
  if (/^mcp__[a-zA-Z0-9_-]+__[a-zA-Z0-9_.*-]+$/.test(token) || token === "mcp__*") {
    return true;
  }

  // Generic wildcard expression
  if (token.includes("*")) {
    return /^[a-zA-Z0-9_:\-.*!]+$/.test(token);
  }

  // Plain tool names like view, grep, bash_output
  return /^[a-zA-Z][a-zA-Z0-9_]*$/.test(token);
}

export function ToolsSelector({
  value: rawValue = [],
  onChange,
  disabled = false,
  className = "",
  description,
  hideLabel = false,
}: ToolsSelectorProps) {
  const value = useMemo(() => normalizeSelection(rawValue), [rawValue]);
  const [isOpen, setIsOpen] = useState(false);
  const [tools, setTools] = useState<ToolInfo[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [customToken, setCustomToken] = useState("");
  const [customError, setCustomError] = useState<string | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Fetch available tools
  useEffect(() => {
    const fetchTools = async () => {
      try {
        const response = await api.tools.list();
        setTools(response.tools || []);
      } catch (error) {
        console.error("Failed to fetch tools:", error);
      } finally {
        setIsLoading(false);
      }
    };
    fetchTools();
  }, []);

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

  // Group tools by category
  const toolsByCategory = useMemo(() => {
    return tools.reduce((acc, tool) => {
      const category = tool.category || "Other";
      if (!acc[category]) {
        acc[category] = [];
      }
      acc[category].push(tool);
      return acc;
    }, {} as Record<string, ToolInfo[]>);
  }, [tools]);

  const availableToolNames = useMemo(() => new Set(tools.map((t) => t.name)), [tools]);

  const selectedConcreteTools = useMemo(
    () => value.filter((token) => availableToolNames.has(token)),
    [value, availableToolNames],
  );

  // Get selection state for a category
  const getCategorySelectionState = (categoryTools: ToolInfo[]): "all" | "some" | "none" => {
    const selectedCount = categoryTools.filter((t) => selectedConcreteTools.includes(t.name)).length;
    if (selectedCount === 0) return "none";
    if (selectedCount === categoryTools.length) return "all";
    return "some";
  };

  const toggleToken = (token: string) => {
    const normalized = token.trim();
    if (!normalized) return;

    if (value.includes(normalized)) {
      onChange(value.filter((t) => t !== normalized));
      return;
    }

    onChange([...value, normalized]);
  };

  // Toggle all tools in a category
  const toggleCategory = (categoryTools: ToolInfo[]) => {
    const state = getCategorySelectionState(categoryTools);
    const categoryToolNames = categoryTools.map((t) => t.name);

    if (state === "all") {
      // Deselect all in this category
      onChange(value.filter((t) => !categoryToolNames.includes(t)));
    } else {
      // Select all in this category (add missing ones)
      const newTools = [...value];
      categoryToolNames.forEach((name) => {
        if (!newTools.includes(name)) {
          newTools.push(name);
        }
      });
      onChange(newTools);
    }
  };

  const toggleTool = (toolName: string) => toggleToken(toolName);

  const removeToken = (token: string) => {
    onChange(value.filter((t) => t !== token));
  };

  const selectAll = () => {
    const expressionTokens = value.filter((token) => !availableToolNames.has(token));
    onChange([...expressionTokens, ...tools.map((t) => t.name)]);
  };

  const clearAll = () => {
    onChange([]);
  };

  const addCustomToken = () => {
    const token = customToken.trim();
    if (!token) return;

    if (!isValidToolToken(token)) {
      setCustomError("Invalid token. Use tool name, tag:*, !*, mcp__server__tool, or wildcard.");
      return;
    }

    setCustomError(null);
    setCustomToken("");
    toggleToken(token);
  };

  const canInteract = !disabled && !isLoading;

  const getTokenStyle = (token: string) => {
    if (token.startsWith("!")) {
      return "bg-red-500/10 text-red-600 border-red-500/20";
    }
    if (token.startsWith("tag:")) {
      return "bg-blue-500/10 text-blue-600 border-blue-500/20";
    }
    if (token.startsWith("mcp__") || token === "tag:mcp" || token === "mcp__*") {
      return "bg-violet-500/10 text-violet-600 border-violet-500/20";
    }
    if (isLikelyExpression(token)) {
      return "bg-amber-500/10 text-amber-700 border-amber-500/20";
    }
    return "bg-primary/10 text-primary border-primary/20";
  };

  return (
    <div className={cn("space-y-2", className)} ref={dropdownRef}>
      {!hideLabel && (
        <div className="flex items-center justify-between">
          <label className="text-sm font-medium text-foreground">Tools</label>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={selectAll}
              disabled={!canInteract}
              className="text-xs text-primary hover:underline disabled:opacity-50"
            >
              Select all tools
            </button>
            <span className="text-xs text-muted-foreground">|</span>
            <button
              type="button"
              onClick={clearAll}
              disabled={!canInteract}
              className="text-xs text-primary hover:underline disabled:opacity-50"
            >
              Clear
            </button>
          </div>
        </div>
      )}

      {description && <p className="text-xs text-muted-foreground">{description}</p>}

      {/* Selected tokens */}
      {value.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {value.map((token) => (
            <span
              key={token}
              className={cn(
                "inline-flex items-center gap-1 px-2 py-0.5 text-xs rounded-full border",
                getTokenStyle(token),
              )}
              title={isLikelyExpression(token) ? "Expression" : "Tool"}
            >
              {token.startsWith("tag:") ? <Tag className="w-3 h-3" /> : <Wrench className="w-3 h-3" />}
              {token}
              {!disabled && (
                <button
                  type="button"
                  onClick={() => removeToken(token)}
                  className="hover:bg-black/10 rounded-full p-0.5"
                >
                  <X className="w-3 h-3" />
                </button>
              )}
            </span>
          ))}
        </div>
      )}

      {/* Dropdown trigger */}
      <div className="relative">
        <button
          type="button"
          onClick={() => canInteract && setIsOpen(!isOpen)}
          disabled={!canInteract}
          className={cn(
            "flex items-center justify-between gap-2 w-full px-3 py-2 text-sm rounded-md",
            "border border-border bg-background",
            "focus:outline-none focus:ring-2 focus:ring-ring",
            !canInteract && "opacity-50 cursor-not-allowed",
          )}
        >
          <span className="text-muted-foreground">
            {isLoading
              ? "Loading tools..."
              : `${value.length} token(s) selected (${selectedConcreteTools.length}/${tools.length} concrete tools)`}
          </span>
          <ChevronDown className="w-4 h-4 opacity-50" />
        </button>

        {/* Dropdown - opens upward since tools are at bottom */}
        {isOpen && (
          <div className="absolute bottom-full left-0 right-0 mb-1 z-50 rounded-md border border-border bg-card shadow-lg">
            <div className="py-2 max-h-80 overflow-y-auto">
              {/* Quick expressions */}
              <div className="px-3 pb-2 border-b border-border/70">
                <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-2 flex items-center gap-1.5">
                  <Sparkles className="w-3 h-3" />
                  Quick expressions
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {QUICK_EXPRESSIONS.map((expr) => {
                    const selected = value.includes(expr.token);
                    return (
                      <button
                        key={expr.token}
                        type="button"
                        onClick={() => toggleToken(expr.token)}
                        className={cn(
                          "text-xs px-2 py-1 rounded border transition-colors",
                          selected
                            ? "bg-primary/10 text-primary border-primary/30"
                            : "bg-background hover:bg-accent border-border",
                        )}
                        title={expr.description}
                      >
                        {expr.token}
                      </button>
                    );
                  })}
                </div>

                <div className="mt-2 flex gap-1.5">
                  <input
                    value={customToken}
                    onChange={(e) => {
                      setCustomToken(e.target.value);
                      if (customError) setCustomError(null);
                    }}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        addCustomToken();
                      }
                    }}
                    placeholder="Add custom token (e.g. mcp__chrome-devtools__click)"
                    className="flex-1 px-2 py-1 text-xs rounded border border-border bg-background focus:outline-none focus:ring-1 focus:ring-ring"
                  />
                  <button
                    type="button"
                    onClick={addCustomToken}
                    className="px-2 py-1 rounded border border-border hover:bg-accent text-xs inline-flex items-center gap-1"
                  >
                    <Plus className="w-3 h-3" />
                    Add
                  </button>
                </div>
                {customError && <p className="text-[11px] text-destructive mt-1">{customError}</p>}
              </div>

              {/* Concrete tools by category */}
              {Object.entries(toolsByCategory).map(([category, categoryTools]) => {
                const selectionState = getCategorySelectionState(categoryTools);
                const selectedInCategory = categoryTools.filter((t) =>
                  selectedConcreteTools.includes(t.name),
                ).length;

                return (
                  <div key={category}>
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        toggleCategory(categoryTools);
                      }}
                      className="w-full px-3 py-1.5 text-xs font-semibold uppercase tracking-wide bg-muted/30 sticky top-0 flex items-center gap-2 hover:bg-muted/50 transition-colors"
                    >
                      {selectionState === "all" ? (
                        <CheckSquare className="w-3.5 h-3.5 text-primary" />
                      ) : selectionState === "some" ? (
                        <MinusSquare className="w-3.5 h-3.5 text-primary" />
                      ) : (
                        <Square className="w-3.5 h-3.5 text-muted-foreground" />
                      )}
                      <span className="flex-1 text-left text-muted-foreground">{category}</span>
                      <span className="text-muted-foreground/70 font-normal normal-case">
                        {selectedInCategory}/{categoryTools.length}
                      </span>
                    </button>
                    {categoryTools.map((tool) => {
                      const isSelected = value.includes(tool.name);
                      return (
                        <button
                          key={tool.name}
                          onClick={() => toggleTool(tool.name)}
                          className={cn(
                            "w-full px-3 py-2 text-left text-sm transition-colors",
                            isSelected ? "bg-accent" : "hover:bg-accent/50",
                          )}
                        >
                          <div className="flex items-start justify-between gap-2">
                            <div className="flex-1 min-w-0">
                              <div className="flex items-center gap-2">
                                <Wrench className="w-3.5 h-3.5 opacity-50 flex-shrink-0" />
                                <span className="font-medium">{tool.name}</span>
                              </div>
                              {tool.description && (
                                <div className="text-xs text-muted-foreground mt-0.5 ml-5.5 line-clamp-1">
                                  {tool.description}
                                </div>
                              )}
                            </div>
                            {isSelected && <Check className="w-4 h-4 text-primary flex-shrink-0 mt-0.5" />}
                          </div>
                        </button>
                      );
                    })}
                  </div>
                );
              })}
              {tools.length === 0 && !isLoading && (
                <div className="px-3 py-4 text-sm text-muted-foreground text-center">
                  No tools available
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export default ToolsSelector;
