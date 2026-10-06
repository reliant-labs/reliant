// Copyright (c) 2025 Reliant Labs

/**
 * "Insert data": a field's picker over everything an expression in it can
 * read — the run's inputs, the trigger, and the outputs of every step that
 * runs before this one (lib/insertableData.ts). Picking one inserts its path
 * at the field's cursor, wrapped in `{{ }}` unless the cursor is already
 * inside a template. It is the discoverable route to the same data the `{{`
 * autocomplete offers.
 */

import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Braces, Plus, Search } from "lucide-react";

import { cn } from "../../lib/utils";
import { ensureNodesCached, getCachedNodes, getNodeDisplayName } from "../../lib/node-metadata";
import { catalogToOutputFields } from "../../lib/nodeOutputFields";
import { insertableData, matchesSearch, type InsertableGroup } from "../../lib/insertableData";
import type { CELCompletionContext } from "../../lib/monaco-cel-completions";
import { useCELCompletionContext, useCELCurrentNode } from "./CELCompletionContext";
import { usePopoverPlacement } from "./usePopoverPlacement";

export interface InsertDataMenuProps {
  /** Insert a CEL path (`nodes.x.response_text`) into the field. */
  onInsert: (path: string) => void;
  /** The field's label, for the menu's accessible name. */
  fieldLabel: string;
  celContext?: CELCompletionContext["celContext"];
  disabled?: boolean;
}

/** The node catalog (each type's outputs) arrives once per session; re-render when it does. */
function useNodeCatalogLoaded(active: boolean): boolean {
  const [loaded, setLoaded] = useState(() => getCachedNodes().length > 0);
  useEffect(() => {
    if (!active || loaded) return;
    let cancelled = false;
    void ensureNodesCached().then(() => {
      if (!cancelled) setLoaded(true);
    });
    return () => {
      cancelled = true;
    };
  }, [active, loaded]);
  return loaded;
}

export function InsertDataMenu({ onInsert, fieldLabel, celContext, disabled = false }: InsertDataMenuProps) {
  const context = useCELCompletionContext();
  const currentNodeId = useCELCurrentNode();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // The highlighted option. Focus stays in the search box (the option is its
  // aria-activedescendant), so typing keeps filtering and Escape is always
  // read by this menu first.
  const [active, setActive] = useState(0);
  const loaded = useNodeCatalogLoaded(open);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const searchId = useId();
  const optionIdPrefix = useId();
  // Search, a max-h-72 list and the note under it.
  const placement = usePopoverPlacement(rootRef, open, 340);

  const groups = useMemo<InsertableGroup[]>(() => {
    if (!open || !context) return [];
    return insertableData({
      context,
      currentNodeId,
      celContext,
      // The same fields the step's Outputs tab lists.
      nodeOutputFields: (nodeType) => catalogToOutputFields(getCachedNodes().find((node) => node.id === nodeType)?.outputFields),
      nodeTypeLabel: getNodeDisplayName,
    });
    // `loaded` re-derives the groups once the node catalog arrives.
  }, [open, context, currentNodeId, celContext, loaded]); // eslint-disable-line react-hooks/exhaustive-deps

  const visible = useMemo(
    () =>
      groups
        .map((group) => ({ ...group, fields: group.fields.filter((field) => matchesSearch(field, group, query)) }))
        .filter((group) => group.fields.length > 0),
    [groups, query],
  );
  const hasSteps = groups.some((group) => group.id.startsWith("node:"));
  const flat = useMemo(() => visible.flatMap((group) => group.fields.map((field) => field.path)), [visible]);
  const activePath = flat[Math.min(active, flat.length - 1)];
  const optionId = (path: string) => `${optionIdPrefix}-${path.replace(/[^\w-]/g, "_")}`;

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
    const onPointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);

  if (!context) return null;

  const close = () => {
    setOpen(false);
    setQuery("");
    setActive(0);
  };

  const choose = (path: string) => {
    onInsert(path);
    close();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      // The builder treats a stray Escape as "leave the builder"; this one
      // only closes the menu.
      event.stopPropagation();
      event.preventDefault();
      close();
      buttonRef.current?.focus();
      return;
    }
    if (event.key === "Enter" && activePath) {
      event.preventDefault();
      choose(activePath);
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    if (flat.length === 0) return;
    setActive((index) => (event.key === "ArrowDown" ? Math.min(index + 1, flat.length - 1) : Math.max(index - 1, 0)));
  };

  return (
    <div ref={rootRef} className="relative inline-flex">
      <button
        ref={buttonRef}
        type="button"
        disabled={disabled}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => (open ? close() : setOpen(true))}
        className="cpv2-mode-pill cpv2-mode-pill-compact"
        title="Insert an input, trigger field or earlier step's output"
      >
        <Plus className="h-3 w-3" aria-hidden />
        Insert data
      </button>

      {open && (
        <div
          role="dialog"
          aria-label={`Insert data into ${fieldLabel.replace(/\s*\*$/, "")}`}
          onKeyDown={onKeyDown}
          data-placement={placement}
          className={cn(
            "absolute right-0 z-[1000] w-[320px] overflow-hidden rounded-lg border border-border bg-card shadow-lg",
            placement === "top" ? "bottom-full mb-1" : "top-full mt-1",
          )}
        >
          <div className="flex items-center gap-2 border-b border-border px-2.5 py-2">
            <Search className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
            <label htmlFor={searchId} className="sr-only">
              Search data
            </label>
            <input
              ref={searchRef}
              id={searchId}
              type="search"
              role="combobox"
              aria-expanded
              aria-controls={`${optionIdPrefix}-list`}
              aria-activedescendant={activePath ? optionId(activePath) : undefined}
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setActive(0);
              }}
              placeholder="Search outputs, inputs and trigger fields"
              className="w-full bg-transparent text-xs text-foreground placeholder:text-muted-foreground focus:outline-none"
            />
          </div>

          <div id={`${optionIdPrefix}-list`} role="listbox" aria-label="Data" className="max-h-72 overflow-y-auto py-1">
            {visible.map((group) => (
              <div key={group.id} role="group" aria-label={group.label}>
                <div className="flex items-baseline gap-1.5 px-2.5 pb-0.5 pt-2">
                  <span className="text-2xs font-semibold uppercase tracking-wider text-muted-foreground">{group.label}</span>
                  {group.detail && <span className="truncate text-2xs text-muted-foreground/80">{group.detail}</span>}
                </div>
                {group.fields.map((field) => (
                  <div
                    key={field.path}
                    id={optionId(field.path)}
                    role="option"
                    aria-selected={field.path === activePath}
                    // Keep focus in the search box; a click still chooses.
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => choose(field.path)}
                    onMouseEnter={() => setActive(flat.indexOf(field.path))}
                    className={cn(
                      "flex w-full cursor-pointer flex-col gap-0.5 px-2.5 py-1.5 text-left",
                      field.path === activePath && "bg-muted",
                    )}
                  >
                    <span className="flex items-center gap-1.5">
                      <Braces className="h-3 w-3 flex-shrink-0 text-muted-foreground" aria-hidden />
                      <code className="truncate font-mono text-xs text-foreground">{field.path}</code>
                      <span className="ml-auto flex-shrink-0 text-2xs text-muted-foreground">{field.type}</span>
                    </span>
                    {field.description && <span className="line-clamp-2 pl-[18px] text-2xs text-muted-foreground">{field.description}</span>}
                  </div>
                ))}
              </div>
            ))}
            {visible.length === 0 && <p className="px-2.5 py-3 text-xs text-muted-foreground">Nothing matches “{query}”.</p>}
            {currentNodeId && !hasSteps && !query && (
              <p className="border-t border-border/60 px-2.5 py-2 text-2xs text-muted-foreground">
                Outputs of steps that run before this one appear here. Connect a step to this one to use what it produces.
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
