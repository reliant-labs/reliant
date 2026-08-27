/**
 * Prompt picker.
 *
 * Surfaces the saved prompts from Settings → Prompts in the composer, where
 * they are actually useful. Until now they could only be created and edited,
 * never used — there was no path from a saved prompt into a message.
 *
 * Picking one inserts its content into the composer rather than sending it, so
 * the prompt is a starting point the user can edit.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { FileText, Loader2, Search } from "lucide-react";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { projectGrpc, type Prompt } from "../../api/project-grpc";

interface PromptPickerProps {
  isOpen: boolean;
  projectId?: string;
  onClose: () => void;
  /** Receives the prompt body to insert into the composer. */
  onSelect: (content: string) => void;
}

export function PromptPicker({
  isOpen,
  projectId,
  onClose,
  onSelect,
}: PromptPickerProps) {
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [query, setQuery] = useState("");
  const [highlighted, setHighlighted] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!isOpen || !projectId) return;

    let cancelled = false;
    setIsLoading(true);
    setQuery("");
    setHighlighted(0);

    projectGrpc
      .getPrompts(projectId)
      .then((loaded) => {
        if (!cancelled) setPrompts(loaded);
      })
      .catch((error) => {
        logger.error("[PromptPicker] Failed to load prompts", error);
        if (!cancelled) setPrompts([]);
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false);
      });

    // Focus after the portal has painted, or the input is not yet in the DOM.
    const timer = setTimeout(() => inputRef.current?.focus(), 30);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [isOpen, projectId]);

  const matches = query.trim()
    ? prompts.filter((prompt) => {
        const haystack =
          `${prompt.name} ${prompt.description} ${prompt.content}`.toLowerCase();
        return haystack.includes(query.toLowerCase());
      })
    : prompts;

  const choose = useCallback(
    (prompt: Prompt) => {
      onSelect(prompt.content);
      onClose();
    },
    [onSelect, onClose],
  );

  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlighted((i) => (matches.length ? (i + 1) % matches.length : 0));
        break;
      case "ArrowUp":
        e.preventDefault();
        setHighlighted((i) =>
          matches.length ? (i - 1 + matches.length) % matches.length : 0,
        );
        break;
      case "Enter":
        e.preventDefault();
        if (matches[highlighted]) choose(matches[highlighted]);
        break;
      case "Escape":
        e.preventDefault();
        onClose();
        break;
    }
  };

  if (!isOpen) return null;

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 pt-[15vh]"
      onMouseDown={onClose}
      // Marks the modal focus context so global shortcuts stand down.
      data-modal-open="true"
    >
      <div
        className="w-full max-w-lg overflow-hidden rounded-lg border border-border bg-popover shadow-xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-border px-3 py-2">
          <Search className="h-4 w-4 shrink-0 text-muted-foreground" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setHighlighted(0);
            }}
            onKeyDown={handleKeyDown}
            placeholder="Search prompts..."
            className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
        </div>

        <div className="max-h-80 overflow-y-auto py-1">
          {isLoading && (
            <div className="flex items-center gap-2 px-3 py-6 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading prompts...
            </div>
          )}

          {!isLoading && matches.length === 0 && (
            <div className="px-3 py-6 text-center text-sm text-muted-foreground">
              {prompts.length === 0
                ? "No saved prompts yet. Add them in Settings → Prompts."
                : "No prompts match that search."}
            </div>
          )}

          {!isLoading &&
            matches.map((prompt, index) => (
              <button
                key={prompt.id}
                type="button"
                role="option"
                aria-selected={index === highlighted}
                className={cn(
                  "flex w-full items-start gap-3 px-3 py-2 text-left transition-colors",
                  index === highlighted ? "bg-accent" : "hover:bg-accent/50",
                )}
                onMouseEnter={() => setHighlighted(index)}
                onClick={() => choose(prompt)}
              >
                <FileText className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium">
                    {prompt.name}
                  </span>
                  <span className="block truncate text-xs text-muted-foreground">
                    {prompt.description || prompt.content}
                  </span>
                </span>
              </button>
            ))}
        </div>

        <div className="border-t border-border px-3 py-1.5 text-xs text-muted-foreground">
          ↑↓ navigate · ↵ insert · esc close
        </div>
      </div>
    </div>,
    document.body,
  );
}
