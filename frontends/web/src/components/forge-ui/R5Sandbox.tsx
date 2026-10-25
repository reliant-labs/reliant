/**
 * The R5 overlay primitives in ForgeTokenSandbox (/forge-token-sandbox):
 * Tooltip, rendered the way its call sites use it, OUTSIDE .forge-ui as they
 * sit in the app. e2e/color-schemes.spec.ts opens each one and screenshots
 * the region with its overlay; the baselines were captured on the pre-R5
 * components, so each R5 commit's image diff is exactly what it changed.
 *
 *   r5-tooltips  ?view=r5-tooltips — a toolbar of icon buttons, a disabled
 *                button that explains itself, a help icon in running text;
 *                the spec hovers the disabled button, then focuses the
 *                toolbar's first button from the keyboard
 *   r5-menus     ?view=r5-menus — a file list with a context menu (the file
 *                tree's and the sidebar's shape: icons, a separator, a
 *                disabled and a danger item) and a help popover beside a
 *                field label; the spec right-clicks a row, then opens the
 *                help
 *   r5-pickers   ?view=r5-pickers — the searchable pickers: a grouped
 *                branch picker (the new-workspace dialog's) and a
 *                multi-select of presets (the workflow settings'); the spec
 *                opens each one
 */
import { useState } from "react";
import { Cloud, Copy, Download, FilePlus, FileText, FolderGit2, HelpCircle, Pencil, RefreshCw, Trash2 } from "lucide-react";

import ContextMenu from "./context_menu";
import { HelpPopover } from "../workflow/HelpPopover";
import Combobox from "./combobox";

import Tooltip from "./tooltip";

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <h2 className="mb-3 text-sm font-medium text-foreground">{children}</h2>;
}

const iconButton =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground";

export function R5Tooltips() {
  return (
    <div className="space-y-6 p-16" data-testid="r5-tooltips">
      <SectionTitle>Tooltip — a toolbar, a disabled action, a help icon</SectionTitle>
      <div className="flex max-w-3xl items-start gap-16">
        <div className="flex items-center gap-1 rounded-lg border border-border bg-card p-1">
          <Tooltip content="Copy path" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Copy path">
              <Copy className="h-4 w-4" />
            </button>
          </Tooltip>
          <Tooltip content="Download" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Download">
              <Download className="h-4 w-4" />
            </button>
          </Tooltip>
          <Tooltip content="Refresh" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Refresh">
              <RefreshCw className="h-4 w-4" />
            </button>
          </Tooltip>
        </div>

        <Tooltip content="Save the workflow first: steps attach to a saved revision, not a draft." side="top">
          <button
            type="button"
            disabled
            className="rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground opacity-50"
          >
            Add step
          </button>
        </Tooltip>

        <p className="flex items-center gap-1 text-sm text-muted-foreground">
          Entry steps run in parallel
          <Tooltip content="Every entry step starts when the workflow does." side="right" triggerClassName="inline-flex">
            <HelpCircle className="h-3.5 w-3.5" aria-label="About entry steps" />
          </Tooltip>
        </p>
      </div>
    </div>
  );
}

const FILES = ["main.go", "go.mod", "README.md"];

export function R5Menus() {
  const [menu, setMenu] = useState<{ x: number; y: number; file: string } | null>(null);
  return (
    <div className="space-y-6 p-16" data-testid="r5-menus" style={{ minHeight: 420 }}>
      <SectionTitle>Context menu and help popover — the file tree, a step field</SectionTitle>
      <div className="flex max-w-3xl items-start gap-16">
        <ul aria-label="Files" className="w-56 rounded-lg border border-border bg-card py-1">
          {FILES.map((file) => (
            <li key={file}>
              <button
                type="button"
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-foreground hover:bg-muted"
                onContextMenu={(e) => {
                  e.preventDefault();
                  setMenu({ x: e.clientX, y: e.clientY, file });
                }}
              >
                <FileText className="h-4 w-4 text-muted-foreground" />
                {file}
              </button>
            </li>
          ))}
        </ul>

        <div className="w-72 space-y-1.5">
          <span className="flex items-center gap-1 text-sm font-medium text-foreground">
            Inject message
            <HelpPopover
              title="Inject message"
              content={"Adds a message to the thread BEFORE the step runs.\n\n• Inherit: added on first execution only\n• New: first message in the new thread"}
            />
          </span>
          <div className="h-8 rounded-md border border-border bg-card" />
        </div>
      </div>
      {menu && (
        <ContextMenu
          label={`${menu.file} actions`}
          open
          position={{ x: menu.x, y: menu.y }}
          onOpenChange={(open) => !open && setMenu(null)}
          groups={[
            {
              items: [
                { label: "Open", icon: <FileText className="h-4 w-4" />, onClick: () => undefined },
                { label: "Rename", icon: <Pencil className="h-4 w-4" />, onClick: () => undefined },
              ],
            },
            {
              items: [
                { label: "New File", icon: <FilePlus className="h-4 w-4" />, onClick: () => undefined, disabled: true },
                { label: "Copy path", icon: <Copy className="h-4 w-4" />, onClick: () => undefined },
              ],
            },
            { items: [{ label: "Delete", icon: <Trash2 className="h-4 w-4" />, onClick: () => undefined, variant: "danger" }] },
          ]}
        />
      )}
    </div>
  );
}

const BRANCHES = [
  { value: "main", label: "main", description: "current", icon: <FolderGit2 className="h-3.5 w-3.5" />, group: "Local Branches" },
  { value: "p9-r5", label: "p9-r5", icon: <FolderGit2 className="h-3.5 w-3.5" />, group: "Local Branches" },
  { value: "origin/main", label: "main", description: "remote", icon: <Cloud className="h-3.5 w-3.5" />, group: "Remote Branches" },
  { value: "origin/release", label: "release", description: "remote", icon: <Cloud className="h-3.5 w-3.5" />, group: "Remote Branches" },
];

const PRESETS = [
  { value: "reviewer", label: "reviewer", description: "Reads the diff and leaves comments" },
  { value: "tester", label: "tester", description: "Writes and runs the tests" },
  { value: "planner", label: "planner", description: "Breaks the work into steps" },
  { value: "docs", label: "docs", description: "Keeps the docs in step" },
];

export function R5Pickers() {
  const [branch, setBranch] = useState<string | null>("main");
  const [presets, setPresets] = useState<string[]>(["reviewer", "tester"]);
  return (
    <div className="space-y-6 p-16" data-testid="r5-pickers" style={{ minHeight: 520 }}>
      <SectionTitle>Searchable pickers — a grouped branch picker, a multi-select</SectionTitle>
      <div className="flex max-w-3xl items-start gap-16">
        <div className="w-80 space-y-2" data-testid="pick-branch">
          <label htmlFor="r5-base-branch" className="block text-sm font-semibold text-foreground">
            Base Branch
          </label>
          <Combobox
            id="r5-base-branch"
            options={BRANCHES}
            value={branch}
            placeholder="Select base branch"
            searchPlaceholder="Search branches"
            emptyMessage="No branches found"
            onChange={setBranch}
            className="w-full"
          />
        </div>
        <div className="w-80 space-y-2" data-testid="pick-presets">
          <span className="block text-sm font-semibold text-foreground">Presets</span>
          <Combobox
            multiple
            aria-label="Presets"
            options={PRESETS}
            value={presets}
            onChange={setPresets}
            placeholder="Select presets..."
            emptyMessage="No presets found"
          />
        </div>
      </div>
    </div>
  );
}
