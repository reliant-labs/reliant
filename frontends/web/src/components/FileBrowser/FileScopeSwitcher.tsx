// Copyright (c) 2025 Reliant Labs

import { ArrowUpToLine } from "lucide-react";

import { cn } from "../../lib/utils";
import { useFileBrowserScopeStore, type FileBrowserScope } from "../../store/fileBrowserScopeStore";
import Tooltip from "../forge-ui/tooltip";

const SCOPES: Array<{ id: FileBrowserScope; label: string; tooltip: string }> = [
  { id: "project", label: "Project", tooltip: "This project or worktree" },
  { id: "scratch", label: "Scratch", tooltip: "This chat's scratch directory (~/.reliant/scratch)" },
  { id: "machine", label: "Machine", tooltip: "The whole machine, read-only" },
];

/**
 * The Files tab's scope switcher: the project tree, the chat's scratch
 * directory, or the whole machine. In the project scope it also offers the
 * way out above the project root (the folder the project is in).
 */
export function FileScopeSwitcher({ hasChat }: { hasChat: boolean }) {
  const scope = useFileBrowserScopeStore((s) => s.scope);
  const setScope = useFileBrowserScopeStore((s) => s.setScope);
  const browseMachine = useFileBrowserScopeStore((s) => s.browseMachine);

  return (
    <div className="flex items-center gap-1 px-2 pt-2">
      <div role="radiogroup" aria-label="Files to show" className="flex flex-1 items-center gap-0.5 min-w-0">
        {SCOPES.map((s) => {
          const disabled = s.id === "scratch" && !hasChat;
          return (
            <Tooltip key={s.id} content={disabled ? "Open a chat to see its scratch directory" : s.tooltip} side="bottom" delay={300} triggerClassName="inline-flex">
              <button
                type="button"
                role="radio"
                aria-checked={scope === s.id}
                disabled={disabled}
                onClick={() => setScope(s.id)}
                className={cn(
                  "rounded-md px-2 py-0.5 text-ui font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-40",
                  scope === s.id ? "bg-foreground/5 text-foreground" : "text-muted-foreground hover:bg-foreground/5 hover:text-foreground",
                )}
              >
                {s.label}
              </button>
            </Tooltip>
          );
        })}
      </div>
      {scope === "project" && (
        <Tooltip content="Show the folder this project is in" side="bottom" delay={300} triggerClassName="inline-flex">
          <button
            type="button"
            aria-label="Show the folder this project is in"
            onClick={() => browseMachine("..")}
            className="rounded-md p-1 text-muted-foreground hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ArrowUpToLine className="w-4 h-4" />
          </button>
        </Tooltip>
      )}
    </div>
  );
}
