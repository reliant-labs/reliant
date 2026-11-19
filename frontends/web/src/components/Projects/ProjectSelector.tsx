// Copyright (c) 2025 Reliant Labs

/**
 * THE PROJECT SELECTOR: the one place a person picks a project or an app —
 * the sidebar's project filter, a new chat's project, the console's app
 * switcher, ⌘K's "Switch project…" (design
 * docs/design/unified-shell-and-projects.md §8.3, owner brief item 4).
 *
 * A command-palette-style panel: a search box, then the org's catalog
 * (services/projects/catalog.ts) grouped Pinned / Recent / All — one group,
 * Results, while searching — each row a project with its app chips and
 * whether a machine has its code, or an app with no code here. New project
 * and Add an app sit at the bottom. The list is virtualized, so an org with
 * hundreds of projects opens and filters as fast as one with five.
 *
 * Keys: type to search; ↑ ↓ (Home, End, PageUp, PageDown) move; Enter
 * picks; Escape closes and gives focus back.
 *
 * Modes:
 *   chat      pick the project a chat is in: projects only, no app-only rows;
 *   navigate  go somewhere: a project row opens its page, an app-only row its
 *             console (the default `onSelect`);
 *   app       pick an app: one row per app (a project holding two lists
 *             both), the console's switcher.
 *
 * Multi-select (`multiple`): choose N of M projects — the chat list's
 * filter. Each row has a checkbox; Enter or a click toggles it and the panel
 * stays open; "Only this" (⇧↵) keeps just that row; "Select all" and "Clear"
 * act on the rows listed (a search narrows them). Grouping is the same.
 *
 * Two presentations of one panel: `ProjectSelector` anchors it to a trigger
 * (forge-ui Popover), `ProjectSelectorDialog` floats it top-centre over the
 * page, like the command palette, for ⌘K and a menu item.
 *
 * Data: one ListProjectsWithApps read (useProjectCatalog). Until it answers,
 * and on a server that cannot answer it (an older one), the picker lists the
 * plain project list the app already holds (catalogFromProjects): it opens
 * at once and keeps working, knowing less. Pins and hides
 * are the person's `sidebar.projects` setting; a hidden row is left out of
 * browsing but a search still finds it.
 */

import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { observeElementRect, useVirtualizer } from "@tanstack/react-virtual";
import { AppWindow, Check, CheckSquare, FolderPlus, Plus, Search, X } from "lucide-react";
import { createPortal } from "react-dom";

import Popover, { type PopoverProps, type PopoverTriggerProps } from "@/components/forge-ui/popover";
import { useModalDialog } from "@/components/forge-ui/modal";
import { useStartBuildingApp } from "@/components/Apps/startBuilding";
import { useProjectCatalog } from "@/hooks/project-catalog-queries";
import { useProjects } from "@/hooks/project-queries";
import { useSidebarProjectPrefs } from "@/hooks/sidebar-project-queries";
import { appPath, projectPath } from "@/lib/paths";
import { isForgeShell } from "@/lib/shell";
import { cn } from "@/lib/utils";
import {
  appEntries,
  appRowKey,
  catalogFromProjects,
  catalogGroups,
  chipStateLabel,
  listCatalog,
  matchesQuery,
  type AppChipModel,
  type AppEntry,
  type CatalogPrefs,
  type ListedRow,
  type ProjectCatalog,
  type ProjectCatalogRow,
} from "@/services/projects/catalog";
import { openAddProject } from "@/store/addProjectDialogStore";

import { AppChip } from "./AppChip";
import { ProjectRowSummary } from "./ProjectRowSummary";

export type ProjectSelectorMode = "chat" | "navigate" | "app";

/** What a person picked. */
export type ProjectSelection =
  | { kind: "project"; row: ProjectCatalogRow }
  | { kind: "app"; app: AppChipModel; project?: ProjectCatalogRow };

/** A fixed choice above the catalog ("All projects", "All apps"). */
export interface ProjectSelectorLeadItem {
  key: string;
  label: string;
  icon?: React.ReactNode;
  selected?: boolean;
  onSelect: () => void;
  testId?: string;
}

/** What a caller says about one project row: a hint beside it, or why it cannot be picked. */
export interface ProjectRowState {
  hint?: string;
  disabled?: boolean;
}

export interface ProjectSelectorPanelProps {
  mode: ProjectSelectorMode;
  /** The org to list; omitted, the tab's (useProjectCatalog). */
  orgId?: string | null;
  /** The current choice, checked: a project id, or `app:<name>` / an app name in app mode. */
  selectedKey?: string | null;
  /** Called with the pick, then the panel closes. Navigate mode navigates when omitted. */
  onSelect?: (selection: ProjectSelection) => void;
  /** Fixed choices above the catalog, hidden while searching unless they match. */
  leadItems?: readonly ProjectSelectorLeadItem[];
  /** A hint or a disabled reason per project row (a new chat on a picked machine). */
  rowState?: (row: ProjectCatalogRow) => ProjectRowState | undefined;
  /** App mode: names to list even when the catalog does not (the URL's app, a list read elsewhere). */
  extraApps?: readonly string[];
  /** The New project / Add an app footer (default on). */
  footer?: boolean;
  /** A line under the list (what it could not read). */
  notice?: React.ReactNode;
  /**
   * Choose several projects (projects only: use with mode "chat"). `selected`
   * is checked; every toggle reports the whole new choice, in the order the
   * person made it. [] is "none chosen".
   */
  multiple?: { selected: readonly string[]; onChange: (projectIds: string[]) => void };
  placeholder?: string;
}

// ── The flat list the virtualizer renders ───────────────────────────────────

type Item =
  | { type: "header"; key: string; label: string }
  | { type: "lead"; key: string; lead: ProjectSelectorLeadItem }
  | { type: "row"; key: string; listed: ListedRow; state?: ProjectRowState }
  | { type: "app"; key: string; entry: AppEntry };

const SIZE: Record<Item["type"], number> = { header: 28, lead: 36, row: 52, app: 52 };
/** The list's height at most; the panel never grows past it. */
const LIST_MAX = 400;

function selectable(item: Item): boolean {
  if (item.type === "header") return false;
  if (item.type === "row") return !item.state?.disabled;
  return true;
}

function headerItem(id: string, label: string): Item {
  return { type: "header", key: `h:${id}`, label };
}

/** The app entries of app mode, with `extra` names the catalog lacks appended. */
function appModeEntries(listed: readonly ListedRow[], extra: readonly string[], catalog: ProjectCatalog | undefined): AppEntry[] {
  const entries = appEntries(listed);
  const seen = new Set(entries.map((e) => e.app.name));
  const state = catalog?.hostedAppsState === "ok" ? "not_deployed" : "unavailable";
  for (const name of extra) {
    if (seen.has(name)) continue;
    seen.add(name);
    entries.push({ app: { name, link: null, state, isStaticSite: false, environments: [] }, pinned: false });
  }
  return entries;
}

export function buildItems(args: {
  mode: ProjectSelectorMode;
  catalog: ProjectCatalog | undefined;
  prefs: CatalogPrefs;
  query: string;
  leadItems?: readonly ProjectSelectorLeadItem[];
  rowState?: (row: ProjectCatalogRow) => ProjectRowState | undefined;
  extraApps?: readonly string[];
}): Item[] {
  const { mode, catalog, prefs, query, leadItems = [], rowState, extraApps = [] } = args;
  const searching = query.trim() !== "";
  const items: Item[] = [];
  for (const lead of leadItems) {
    if (searching && !lead.label.toLowerCase().includes(query.trim().toLowerCase())) continue;
    items.push({ type: "lead", key: `lead:${lead.key}`, lead });
  }
  const view = { query, showHidden: searching, kinds: mode === "chat" ? "projects" : mode === "app" ? "apps" : "any" } as const;
  const listed = catalog ? listCatalog(catalog, prefs, view) : [];

  if (mode === "app") {
    // An app matches on its own name, or on its project's (name, repos,
    // folder) — not on a sibling app's name, which the project row also holds.
    const entries = appModeEntries(listed, extraApps, catalog).filter(
      (e) =>
        !searching ||
        matchesQuery({ kind: "app", key: appRowKey(e.app.name), name: e.app.name, activityAt: 0, app: e.app }, query) ||
        (e.project !== undefined && matchesQuery({ ...e.project, apps: [] }, query)),
    );
    const pinned = entries.filter((e) => e.pinned);
    const rest = entries.filter((e) => !e.pinned);
    const push = (id: string, label: string, list: AppEntry[]) => {
      if (list.length === 0) return;
      items.push(headerItem(id, label));
      for (const entry of list) items.push({ type: "app", key: `app:${entry.app.name}`, entry });
    };
    if (searching) push("results", "Results", entries);
    else {
      push("pinned", "Pinned", pinned);
      push("apps", "Apps", rest);
    }
    return items;
  }

  for (const group of catalogGroups(listed, { searching })) {
    items.push(headerItem(group.id, group.label));
    for (const l of group.rows) {
      items.push({ type: "row", key: l.row.key, listed: l, state: l.row.kind === "project" ? rowState?.(l.row) : undefined });
    }
  }
  return items;
}

// ── Data ────────────────────────────────────────────────────────────────────

/**
 * The catalog; until it answers, or where the server cannot answer it, the
 * plain project list the app already holds (catalogFromProjects), so the
 * selector opens on rows at once and fills in apps and checkouts when the
 * catalog lands.
 */
function useSelectorCatalog(orgId: string | null | undefined): { catalog: ProjectCatalog | undefined; loading: boolean } {
  const view = useProjectCatalog(orgId);
  const plain = useProjects();
  return useMemo(() => {
    if (view.catalog) return { catalog: view.catalog, loading: false };
    const plainReady = !plain.isLoading && (plain.projects.length > 0 || view.error !== null);
    if (plainReady) return { catalog: catalogFromProjects(orgId ?? "", plain.projects), loading: false };
    if (isForgeShell()) return { catalog: undefined, loading: false };
    return { catalog: undefined, loading: view.isLoading || plain.isLoading };
  }, [view.catalog, view.isLoading, view.error, plain.isLoading, plain.projects, orgId]);
}

// ── The panel ───────────────────────────────────────────────────────────────

const ROW_CLASS =
  "flex w-full cursor-pointer items-center gap-2 rounded-md px-2 text-left text-ui text-foreground aria-disabled:cursor-not-allowed aria-disabled:opacity-60";

function AppEntrySummary({ entry }: { entry: AppEntry }) {
  return (
    <div className="flex min-w-0 flex-1 items-center gap-3" data-testid="app-entry-summary">
      <AppWindow aria-hidden className="h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-ui font-medium text-foreground">{entry.app.name}</span>
        <span className="truncate text-meta text-muted-foreground">
          {entry.project ? `In ${entry.project.name}` : "App without code here"}
          {entry.app.state !== "unavailable" ? ` · ${chipStateLabel(entry.app.state)}` : ""}
        </span>
      </div>
      <AppChip app={entry.app} interactive={false} />
    </div>
  );
}

export function ProjectSelectorPanel({
  mode,
  orgId,
  selectedKey,
  onSelect,
  leadItems,
  rowState,
  extraApps,
  footer = true,
  notice,
  multiple,
  placeholder,
  onClose,
  inputRef: givenInputRef,
}: ProjectSelectorPanelProps & { onClose: () => void; inputRef?: React.RefObject<HTMLInputElement | null> }) {
  const navigate = useNavigate();
  const startBuilding = useStartBuildingApp();
  const { catalog, loading } = useSelectorCatalog(orgId);
  const { prefs } = useSidebarProjectPrefs();
  const [query, setQuery] = useState("");
  const ownInputRef = useRef<HTMLInputElement | null>(null);
  const inputRef = givenInputRef ?? ownInputRef;
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const listId = useId();

  const items = useMemo(
    () => buildItems({ mode, catalog, prefs, query, leadItems, rowState, extraApps }),
    [mode, catalog, prefs, query, leadItems, rowState, extraApps],
  );

  // The active option, by key so it survives the list refetching: the one
  // the person moved to, else (before any search) the current choice, else
  // the first. A new search starts again from the first match.
  const [activeKey, setActiveKey] = useState<string | null>(null);
  const active = useMemo(() => {
    let i = activeKey ? items.findIndex((it) => it.key === activeKey && selectable(it)) : -1;
    if (i < 0 && !query.trim() && selectedKey) i = items.findIndex((it) => selectable(it) && keyOf(it) === selectedKey);
    if (i < 0) i = items.findIndex(selectable);
    return i;
  }, [items, activeKey, query, selectedKey]);
  const firstSelectable = useCallback((list: Item[]) => list.findIndex(selectable), []);

  const virtualizer = useVirtualizer({
    count: items.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (i) => SIZE[items[i].type],
    getItemKey: (i) => items[i].key,
    overscan: 8,
    initialRect: { width: 480, height: LIST_MAX },
    // A list not laid out yet (opening, or a test's DOM) measures 0 tall and
    // would render no rows: treat it as full height until it measures.
    observeElementRect: (instance, cb) =>
      observeElementRect(instance, (rect) => cb(rect.height > 0 ? rect : { width: rect.width || 480, height: LIST_MAX })),
  });

  const move = (from: number, step: number): number => {
    if (items.length === 0) return -1;
    let i = from;
    for (let n = 0; n < items.length; n++) {
      i = Math.min(Math.max(i + step, 0), items.length - 1);
      if (selectable(items[i])) return i;
      if ((step < 0 && i === 0) || (step > 0 && i === items.length - 1)) break;
    }
    return from;
  };
  const activate = (i: number) => {
    if (i < 0 || !items[i]) return;
    setActiveKey(items[i].key);
    virtualizer.scrollToIndex(i, { align: "auto" });
  };

  // Opening on a choice far down the list shows it.
  const scrolledToChoice = useRef(false);
  useEffect(() => {
    if (scrolledToChoice.current || active <= 0 || items.length === 0) return;
    scrolledToChoice.current = true;
    virtualizer.scrollToIndex(active, { align: "center" });
  }, [active, items.length, virtualizer]);

  const chosen = useMemo(() => new Set(multiple?.selected ?? []), [multiple?.selected]);
  // The project rows listed now: what Select all and Clear act on.
  const listedProjectIds = useMemo(
    () => items.flatMap((i) => (i.type === "row" && i.listed.row.kind === "project" && selectable(i) ? [i.listed.row.key] : [])),
    [items],
  );
  const toggle = (id: string) => {
    if (!multiple) return;
    const now = multiple.selected;
    multiple.onChange(chosen.has(id) ? now.filter((x) => x !== id) : [...now, id]);
  };
  const onlyThis = (id: string) => multiple?.onChange([id]);
  const selectAllListed = () => {
    if (!multiple) return;
    multiple.onChange([...multiple.selected, ...listedProjectIds.filter((id) => !chosen.has(id))]);
  };
  const clearListed = () => {
    if (!multiple) return;
    const listed = new Set(listedProjectIds);
    multiple.onChange(query.trim() ? multiple.selected.filter((id) => !listed.has(id)) : []);
  };

  const pick = (item: Item) => {
    if (!selectable(item)) return;
    if (multiple && item.type === "row") {
      toggle(item.listed.row.key);
      return;
    }
    onClose();
    if (item.type === "lead") {
      item.lead.onSelect();
      return;
    }
    if (item.type === "header") return;
    let selection: ProjectSelection;
    if (item.type === "app") selection = { kind: "app", app: item.entry.app, project: item.entry.project };
    else {
      const { row } = item.listed;
      selection = row.kind === "project" ? { kind: "project", row } : { kind: "app", app: row.app };
    }
    if (onSelect) {
      onSelect(selection);
      return;
    }
    if (mode !== "navigate") return;
    void navigate({ to: selection.kind === "project" ? projectPath(selection.row.key) : appPath(selection.app.name) });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const page = Math.max(1, Math.floor(LIST_MAX / SIZE.row));
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        activate(active < 0 ? firstSelectable(items) : move(active, 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        activate(move(active, -1));
        break;
      case "PageDown":
        e.preventDefault();
        activate(move(Math.min(active + page - 1, items.length - 1), 1));
        break;
      case "PageUp":
        e.preventDefault();
        activate(move(Math.max(active - page + 1, 0), -1));
        break;
      case "Home":
        if (e.ctrlKey || e.metaKey || query === "") {
          e.preventDefault();
          activate(firstSelectable(items));
        }
        break;
      case "End":
        if (e.ctrlKey || e.metaKey || query === "") {
          e.preventDefault();
          activate(move(items.length, -1));
        }
        break;
      case "Enter":
        if (active >= 0 && items[active]) {
          e.preventDefault();
          const item = items[active];
          if (multiple && e.shiftKey && item.type === "row") onlyThis(item.listed.row.key);
          else pick(item);
        }
        break;
    }
  };

  const optionId = (i: number) => `${listId}-o-${i}`;
  const total = virtualizer.getTotalSize();
  const empty = !loading && items.every((i) => i.type === "header");
  const label = mode === "app" ? "Apps" : "Projects";

  return (
    <div className="flex w-[min(32rem,calc(100vw-1rem))] flex-col" data-testid="project-selector" data-mode={mode}>
      <div className="flex items-center gap-2 border-b border-border px-3">
        <Search aria-hidden className="h-4 w-4 shrink-0 text-muted-foreground" />
        <input
          ref={inputRef}
          role="combobox"
          aria-expanded
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={active >= 0 ? optionId(active) : undefined}
          aria-label={placeholder ?? (mode === "app" ? "Search apps" : "Search projects")}
          data-testid="project-selector-search"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActiveKey(null);
          }}
          onKeyDown={onKeyDown}
          placeholder={placeholder ?? (mode === "app" ? "Search apps…" : "Search projects, repos and apps…")}
          className="h-11 min-w-0 flex-1 bg-transparent text-ui text-foreground placeholder:text-muted-foreground focus:outline-none"
          autoComplete="off"
          spellCheck={false}
        />
      </div>

      {multiple ? (
        <div className="flex items-center gap-1 border-b border-border px-2 py-1" data-testid="project-selector-multi-bar">
          <span className="px-1 text-meta text-muted-foreground" data-testid="project-selector-chosen-count">
            {multiple.selected.length === 0 ? "All projects" : `${multiple.selected.length} chosen`}
          </span>
          <span className="ml-auto flex items-center gap-1">
            <FooterButton icon={<CheckSquare aria-hidden className="h-3.5 w-3.5" />} testId="project-selector-select-all" onClick={selectAllListed}>
              Select all
            </FooterButton>
            <FooterButton icon={<X aria-hidden className="h-3.5 w-3.5" />} testId="project-selector-clear" onClick={clearListed}>
              Clear
            </FooterButton>
          </span>
        </div>
      ) : null}
      <div
        ref={scrollRef}
        id={listId}
        role="listbox"
        aria-label={label}
        aria-multiselectable={multiple ? true : undefined}
        className="overflow-y-auto overscroll-contain p-1"
        style={{ height: empty || loading ? undefined : Math.min(total + 8, LIST_MAX) }}
      >
        {loading ? (
          <p className="px-3 py-4 text-meta text-muted-foreground" data-testid="project-selector-loading">
            Loading {label.toLowerCase()}…
          </p>
        ) : empty ? (
          <p className="px-3 py-4 text-meta text-muted-foreground" data-testid="project-selector-empty">
            {query.trim()
              ? `Nothing matches “${query.trim()}”.`
              : mode === "app"
                ? "No apps in this org yet."
                : "No projects yet."}
          </p>
        ) : (
          <div style={{ height: total, position: "relative" }}>
            {virtualizer.getVirtualItems().map((v) => {
              const item = items[v.index];
              const style: React.CSSProperties = { position: "absolute", top: 0, left: 0, width: "100%", height: v.size, transform: `translateY(${v.start}px)` };
              if (item.type === "header") {
                return (
                  <div key={v.key} role="presentation" style={style} className="flex items-end px-2 pb-1 text-meta font-medium text-muted-foreground">
                    {item.label}
                  </div>
                );
              }
              const isActive = v.index === active;
              const selected = multiple
                ? item.type === "row" && chosen.has(item.listed.row.key)
                : selectedKey != null && keyOf(item) === selectedKey;
              const disabled = !selectable(item);
              return (
                <div
                  key={v.key}
                  id={optionId(v.index)}
                  role="option"
                  aria-selected={selected}
                  aria-disabled={disabled || undefined}
                  data-testid={testIdOf(item)}
                  data-active={isActive || undefined}
                  style={style}
                  onPointerDown={(e) => e.preventDefault()}
                  onPointerMove={() => !disabled && !isActive && setActiveKey(item.key)}
                  onClick={() => pick(item)}
                  className={cn(ROW_CLASS, isActive && "bg-foreground/5")}
                >
                  {multiple ? (
                    <span
                      aria-hidden
                      data-testid="project-selector-checkbox"
                      className={cn(
                        "grid h-4 w-4 shrink-0 place-items-center rounded-md border",
                        selected ? "border-primary bg-primary text-primary-foreground" : "border-border",
                      )}
                    >
                      {selected ? <Check className="h-3 w-3" /> : null}
                    </span>
                  ) : (
                    <Check aria-hidden className={cn("h-3.5 w-3.5 shrink-0 text-primary-ink", !selected && "invisible")} />
                  )}
                  {item.type === "lead" ? (
                    <span className="flex min-w-0 flex-1 items-center gap-2">
                      {item.lead.icon}
                      <span className="truncate">{item.lead.label}</span>
                    </span>
                  ) : item.type === "app" ? (
                    <AppEntrySummary entry={item.entry} />
                  ) : (
                    <>
                      {/* Choosing several, the row also holds a checkbox and Only this: one chip. */}
                      <ProjectRowSummary row={item.listed.row} pinned={item.listed.pinned} maxChips={multiple ? 1 : 3} />
                      {item.state?.hint ? <span className="ml-2 shrink-0 text-meta text-muted-foreground">{item.state.hint}</span> : null}
                      {multiple && isActive ? (
                        <button
                          type="button"
                          tabIndex={-1}
                          data-testid="project-selector-only"
                          aria-label={`Only ${item.listed.row.name}`}
                          onClick={(e) => {
                            e.stopPropagation();
                            onlyThis(item.listed.row.key);
                          }}
                          className="ml-1 shrink-0 rounded-md px-1.5 py-0.5 text-meta text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
                        >
                          Only this
                        </button>
                      ) : null}
                    </>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      {notice ? (
        <p className="border-t border-border px-3 py-2 text-meta text-muted-foreground" data-testid="project-selector-notice">
          {notice}
        </p>
      ) : null}
      {footer ? (
        <div className="flex items-center gap-1 border-t border-border p-1">
          {mode !== "app" ? (
            <FooterButton
              icon={<FolderPlus aria-hidden className="h-3.5 w-3.5" />}
              testId="project-selector-new-project"
              onClick={() => {
                onClose();
                openAddProject();
              }}
            >
              New project
            </FooterButton>
          ) : null}
          {mode !== "chat" ? (
            <FooterButton
              icon={<Plus aria-hidden className="h-3.5 w-3.5" />}
              testId="project-selector-add-app"
              onClick={() => {
                onClose();
                startBuilding();
              }}
            >
              Add an app
            </FooterButton>
          ) : null}
          {multiple ? (
            <>
              <span className="ml-auto hidden px-2 text-meta text-muted-foreground sm:inline">↵ to choose · ⇧↵ only this</span>
              <FooterButton icon={<Check aria-hidden className="h-3.5 w-3.5" />} testId="project-selector-done" onClick={onClose}>
                Done
              </FooterButton>
            </>
          ) : (
            <span className="ml-auto hidden px-2 text-meta text-muted-foreground sm:inline">↑↓ to move · ↵ to pick · esc to close</span>
          )}
        </div>
      ) : null}
    </div>
  );
}

function FooterButton({ icon, onClick, testId, children }: { icon: React.ReactNode; onClick: () => void; testId: string; children: React.ReactNode }) {
  return (
    <button
      type="button"
      data-testid={testId}
      onClick={onClick}
      className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-ui text-muted-foreground hover:bg-foreground/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      {icon}
      {children}
    </button>
  );
}

/** The key `selectedKey` is compared with: a project id, `app:<name>`, or (app mode) the app's name too. */
function keyOf(item: Item): string | undefined {
  if (item.type === "row") return item.listed.row.key;
  if (item.type === "app") return item.entry.app.name;
  if (item.type === "lead") return item.lead.key;
  return undefined;
}

function testIdOf(item: Item): string | undefined {
  if (item.type === "lead") return item.lead.testId ?? `project-selector-lead-${item.lead.key}`;
  if (item.type === "app") return `project-selector-app-${item.entry.app.name}`;
  if (item.type === "row") return `project-selector-row-${item.listed.row.key}`;
  return undefined;
}

// ── Presentations ───────────────────────────────────────────────────────────

export interface ProjectSelectorProps extends ProjectSelectorPanelProps {
  /** The control that opens it; spread the props on your element. */
  trigger: (props: PopoverTriggerProps) => React.ReactElement;
  align?: PopoverProps["align"];
  side?: PopoverProps["side"];
  disabled?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

/** The selector anchored to a trigger. */
export function ProjectSelector({ trigger, align, side, disabled, open: controlled, onOpenChange, ...panel }: ProjectSelectorProps) {
  const [own, setOwn] = useState(false);
  const open = controlled ?? own;
  const setOpen = useCallback(
    (next: boolean) => {
      if (controlled === undefined) setOwn(next);
      onOpenChange?.(next);
    },
    [controlled, onOpenChange],
  );
  const inputRef = useRef<HTMLInputElement | null>(null);
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger={trigger}
      align={align}
      side={side}
      disabled={disabled}
      label={panel.mode === "app" ? "Switch app" : "Choose a project"}
      panelClassName="overflow-hidden"
      onOpenFocus={() => inputRef.current?.focus({ preventScroll: true })}
    >
      {({ close }) => <ProjectSelectorPanel {...panel} inputRef={inputRef} onClose={close} />}
    </Popover>
  );
}

export interface ProjectSelectorDialogProps extends ProjectSelectorPanelProps {
  open: boolean;
  onClose: () => void;
  /** The dialog's accessible name. */
  label?: string;
}

/** The selector over the page, top-centre: ⌘K's Switch project…, a menu item. */
export function ProjectSelectorDialog({ open, onClose, label, ...panel }: ProjectSelectorDialogProps) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  useModalDialog({ open, panelRef, onClose, initialFocus: () => inputRef.current });
  if (!open || typeof document === "undefined") return null;
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-background/70 px-2 pt-[10vh]" data-testid="project-selector-dialog" onPointerDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={label ?? (panel.mode === "app" ? "Switch app" : "Switch project")}
        className="overflow-hidden rounded-xl border border-border bg-popover shadow-lg"
      >
        <ProjectSelectorPanel {...panel} inputRef={inputRef} onClose={onClose} />
      </div>
    </div>,
    document.body,
  );
}
