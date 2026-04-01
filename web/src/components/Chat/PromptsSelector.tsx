import { useState, useEffect, useRef } from "react";
import { ChevronDown, Check, FileText } from "lucide-react";
import { projectGrpc, type Prompt as GrpcPrompt } from "../../api/project-grpc";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";

interface Prompt {
  id: string;
  title: string;
  name: string;  // gRPC uses 'name' instead of 'title'
  content: string;
  default?: boolean;
  hotkey?: string;
}

interface PromptsSelectorProps {
  projectId?: string;
  onPromptsChange: (prompts: string) => void;
  compact?: boolean;
  iconOnly?: boolean;
  dropdownAlign?: "left" | "right";
}

export function PromptsSelector({
  projectId,
  onPromptsChange,
  compact = false,
  iconOnly = false,
  dropdownAlign = "left",
}: PromptsSelectorProps) {
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [selectedPrompts, setSelectedPrompts] = useState<Set<string>>(
    new Set()
  );
  const [isExpanded, setIsExpanded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const loadPrompts = async () => {
      if (!projectId) {
        setPrompts([]);
        return;
      }

      setLoading(true);
      setError(null);
      try {
        const grpcPrompts = await projectGrpc.getPrompts(projectId);
        // Convert gRPC prompts to UI format (title = name)
        const promptsData = grpcPrompts.map((p: GrpcPrompt) => ({
          id: p.id,
          title: p.name,
          name: p.name,
          content: p.content,
          default: false,  // gRPC doesn't have default flag
        }));

        setPrompts(promptsData);

        // Set default selections (none by default since gRPC doesn't track this)
        const defaults = new Set<string>();
        setSelectedPrompts(defaults);
      } catch (error) {
        setError("Failed to load prompts");
      } finally {
        setLoading(false);
      }
    };

    loadPrompts();
  }, [projectId]);

  useEffect(() => {
    // Merge selected prompts content
    const selectedContent = prompts
      .filter((p) => selectedPrompts.has(p.id))
      .map((p) => p.content)
      .join("\n\n");
    onPromptsChange(selectedContent);
  }, [selectedPrompts, prompts, onPromptsChange]);

  const togglePrompt = (promptId: string) => {
    setSelectedPrompts((prev) => {
      const newSet = new Set(prev);
      if (newSet.has(promptId)) {
        newSet.delete(promptId);
      } else {
        newSet.add(promptId);
      }
      return newSet;
    });
  };

  // Auto-scroll to first selected item when dropdown opens
  useEffect(() => {
    if (isExpanded && dropdownRef.current) {
      const selectedButton = dropdownRef.current.querySelector(".bg-accent");
      if (selectedButton) {
        selectedButton.scrollIntoView({ block: "center", behavior: "auto" });
      }
    }
  }, [isExpanded]);

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(event.target as Node)
      ) {
        setIsExpanded(false);
      }
    };

    if (isExpanded) {
      document.addEventListener("mousedown", handleClickOutside);
      return () =>
        document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isExpanded]);

  const selectedCount = selectedPrompts.size;

  // Compact mode - minimal button with dropdown
  if (compact) {
    return (
      <div
        className="relative"
        ref={dropdownRef}
        data-dropdown-open={isExpanded}
      >
        <Tooltip
          content="Select system prompts to include with your messages"
          placement="top"
        >
          <button
            onClick={() => setIsExpanded(!isExpanded)}
            className={cn(
              "chat-button flex items-center rounded text-[10px] font-medium transition-colors h-6",
              iconOnly
                ? "px-1 w-6 justify-center"
                : "gap-1 px-1.5 py-1 min-w-[28px]",
              "bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] hover:bg-[var(--chat-button-hover)]"
            )}
            disabled={loading}
          >
            <FileText className="w-3 h-3" />
            {!iconOnly && (
              <>
                <span className="truncate">
                  {selectedCount > 0
                    ? `${selectedCount}/${prompts.length}`
                    : "None"}
                </span>
                <ChevronDown
                  className={cn(
                    "w-3 h-3 opacity-50",
                    isExpanded && "rotate-180"
                  )}
                />
              </>
            )}
          </button>
        </Tooltip>

        {isExpanded && (
          <div
            className={`absolute bottom-full mb-1 ${
              dropdownAlign === "right" ? "right-0" : "left-0"
            } w-64 border border-border/50 rounded-md shadow-lg z-[1000] bg-[var(--chat-dropdown-bg)]`}
          >
            <div className="py-1 max-h-80 overflow-y-auto rounded-md">
                {prompts.length === 0 ? (
                  <div className="px-3 py-2 text-xs text-[var(--chat-button-text)]">
                    <div>No prompts configured</div>
                    <button
                      onClick={(e) => {
                        e.preventDefault();
                        window.dispatchEvent(
                          new CustomEvent("open-settings-prompts")
                        );
                        setIsExpanded(false);
                      }}
                      className="text-primary hover:underline mt-1"
                    >
                      Configure in Settings
                    </button>
                  </div>
                ) : (
                  <>
                    {/* No prompts option */}
                    <button
                      onClick={() => {
                        setSelectedPrompts(new Set());
                        setIsExpanded(false);
                      }}
                      className={cn(
                        "w-full px-3 py-2 text-left text-xs transition-colors border-b border-border/20 text-foreground",
                        selectedCount === 0
                          ? "bg-accent font-semibold"
                          : "hover:bg-accent/50"
                      )}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="flex-1">
                          <div className="font-semibold mb-1">No prompts</div>
                          <div className="text-muted-foreground text-[11px] leading-snug">
                            Send messages without any system prompts
                          </div>
                        </div>
                        {selectedCount === 0 && (
                          <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                        )}
                      </div>
                    </button>
                    {prompts.map((prompt) => (
                      <button
                        key={prompt.id}
                        onClick={() => togglePrompt(prompt.id)}
                        className={cn(
                          "w-full px-3 py-2 text-left text-xs transition-colors border-b border-border/20 last:border-0 text-foreground",
                          selectedPrompts.has(prompt.id)
                            ? "bg-accent font-semibold"
                            : "hover:bg-accent/50"
                        )}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="flex-1">
                            <div className="font-semibold mb-1 flex items-center gap-1">
                              {prompt.title}
                              {prompt.hotkey && (
                                <kbd className="px-1 py-0.5 text-[9px] font-mono bg-muted rounded border border-border/20">
                                  {prompt.hotkey}
                                </kbd>
                              )}
                            </div>
                            <div className="text-muted-foreground text-[10px] leading-snug line-clamp-2">
                              {prompt.content}
                            </div>
                          </div>
                          {selectedPrompts.has(prompt.id) && (
                            <Check className="w-3 h-3 text-primary flex-shrink-0 mt-0.5" />
                          )}
                        </div>
                      </button>
                    ))}
                  </>
                )}
              </div>
            </div>
        )}
      </div>
    );
  }

  // Original full-width mode for other uses
  if (prompts.length === 0 && !loading && !error) {
    return (
      <div
        className="border border-border/20 rounded-lg opacity-60"
        style={{ backgroundColor: "var(--chat-button-bg)" }}
      >
        <div className="px-4 py-2 text-sm font-mono text-muted-foreground flex items-center justify-between">
          <span>No prompts yet</span>
          <a
            href="#/settings/prompts"
            className="text-primary hover:underline"
            onClick={(e) => {
              e.preventDefault();
              window.dispatchEvent(new CustomEvent("open-settings-prompts"));
            }}
          >
            Configure in Settings
          </a>
        </div>
      </div>
    );
  }

  return (
    <div
      className="border border-border/20 rounded-lg"
      style={{ backgroundColor: "var(--chat-button-bg)" }}
    >
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full px-4 py-2 flex items-center justify-between hover:bg-accent/50 transition-colors"
        disabled={loading}
      >
        <div className="flex items-center gap-2 text-sm font-mono">
          <span className="text-muted-foreground">
            Prompts ({prompts.length} available):
          </span>
          {loading && <span className="text-muted-foreground">Loading...</span>}
          {error && <span className="text-destructive">Error</span>}
          {!loading && !error && selectedCount > 0 && (
            <span className="text-primary">{selectedCount} selected</span>
          )}
        </div>
        {isExpanded ? (
          <ChevronDown className="w-4 h-4 text-muted-foreground rotate-180" />
        ) : (
          <ChevronDown className="w-4 h-4 text-muted-foreground" />
        )}
      </button>

      {isExpanded && (
        <div className="border-t border-border/20 p-3 space-y-1 max-h-64 overflow-y-auto">
          {prompts.map((prompt) => (
            <label
              key={prompt.id}
              className="flex items-start gap-2 p-2 rounded hover:bg-accent/30 cursor-pointer transition-colors"
            >
              <input
                type="checkbox"
                checked={selectedPrompts.has(prompt.id)}
                onChange={() => togglePrompt(prompt.id)}
                className="mt-0.5 rounded border-input"
              />
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-mono">{prompt.title}</span>
                  {prompt.hotkey && (
                    <kbd className="px-1.5 py-0.5 text-xs font-mono bg-[var(--chat-button-bg)] rounded border border-border/20">
                      {prompt.hotkey}
                    </kbd>
                  )}
                </div>
                {selectedPrompts.has(prompt.id) && (
                  <div className="mt-1 text-xs text-muted-foreground font-mono truncate">
                    {prompt.content.split("\n")[0]}...
                  </div>
                )}
              </div>
              {selectedPrompts.has(prompt.id) && (
                <Check className="w-4 h-4 text-primary mt-0.5" />
              )}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
