import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  Camera,
  ChevronLeft,
  ChevronRight,
  FileText,
  Gauge,
  Image as ImageIcon,
  Paperclip,
  Slash,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { isCelTemplate } from "../../lib/celTemplate";
import type { Preset } from "../../hooks/preset-queries";
import { PresetList } from "../workflow/InlinePresetPicker";
import { ComposerSheet } from "./ComposerSheet";
import { ComposerWorkflowPicker, useComposerWorkflows } from "./ComposerWorkflowPicker";
import {
  AccountUsageDetails,
  accountUsageLabel,
  accountUsageTone,
  useAccountUsageChip,
} from "./AccountUsageChip";
import type { SlashCommand } from "./SlashCommandMenu";
import type { PageId as SettingsPageId } from "./settings/ChatSettingsPopover";

/**
 * The ⊕ sheet: "Chat options" (design doc §5.4).
 *
 * The one place for everything the composer configures. Pages push INSIDE
 * this one sheet — Model, a preset, the workflow, all settings, commands —
 * each with a back arrow to the root, so drilling in never closes the sheet
 * and drops the user's place to open another one. forge-ui's Modal has no
 * page stack of its own, so the stack is this component's content; the
 * Modal stays the shell (focus trap, Escape, scroll lock, portal).
 */

export type ComposerOptionsPage =
  | { id: "root" }
  | { id: "model" }
  | { id: "settings"; initial: Extract<SettingsPageId, "main" | "params"> }
  | { id: "preset"; target: string }
  | { id: "workflow" }
  | { id: "commands" }
  | { id: "account" };

export type AttachKind = "photos" | "camera" | "files";

export interface PresetRow {
  /** "" for the workflow-level target, else the group name. */
  target: string;
  label: string;
  value: string | null;
  presets: Preset[];
  loading: boolean;
}

export interface ComposerOptionsProps {
  page: ComposerOptionsPage;
  onNavigate: (page: ComposerOptionsPage) => void;
  onClose: () => void;
  /** Attach tiles; null when this surface cannot attach. */
  attach: null | {
    /** Offer the camera tile (a coarse pointer: a device with one at hand). */
    camera: boolean;
    disabled: boolean;
    onPick: (kind: AttachKind) => void;
  };
  /** Workflow settings; null when this surface cannot configure them. */
  configure: null | {
    status: "loading" | "error" | "empty" | "ready";
    onRetry: () => void;
    hasUnsetParams: boolean;
    /** Current model, for the Model row; null when the workflow takes none. */
    model: ReactNode | null;
    /** The shared settings content (ChatSettingsPopover, embedded) on a page. */
    renderSettings: (initialPage: SettingsPageId, onExit: () => void) => ReactNode;
  };
  presets: PresetRow[];
  onPresetChange: (target: string, preset: Preset | null) => void;
  workflow: {
    value: string | null;
    /** The chat has started: the workflow is fixed. */
    locked: boolean;
    disabled: boolean;
    onChange: (workflow: string | null) => void;
  };
  onPrompts: () => void;
  commands: SlashCommand[];
  onCommand: (command: SlashCommand) => void;
  chatId?: string;
}

const GROUP = "overflow-hidden rounded-xl border border-border/60 bg-background";
const ROW =
  "flex min-h-12 w-full items-center gap-3 border-b border-border/60 px-4 py-2.5 text-left text-sm last:border-b-0 hover:bg-foreground/5 active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60";

function Row({
  label,
  value,
  onClick,
  disabled,
  icon,
  badge,
  testId,
}: {
  label: string;
  value?: ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  icon?: ReactNode;
  badge?: ReactNode;
  testId?: string;
}) {
  const content = (
    <>
      {icon && <span className="flex shrink-0 items-center text-muted-foreground">{icon}</span>}
      <span className="flex min-w-0 flex-1 items-center gap-2 font-medium text-foreground">
        {label}
        {badge}
      </span>
      {value !== undefined && <span className="min-w-0 max-w-[55%] truncate text-right text-muted-foreground">{value}</span>}
      {onClick && <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />}
    </>
  );
  if (!onClick) {
    return <div className={cn(ROW, "hover:bg-transparent active:bg-transparent")} data-testid={testId}>{content}</div>;
  }
  return (
    <button type="button" className={ROW} onClick={onClick} disabled={disabled} data-testid={testId}>
      {content}
    </button>
  );
}

/** A pushed page's header: back to the root, and the page's title. */
function PageHeader({ title, onBack }: { title: string; onBack: () => void }) {
  return (
    <div className="mb-2 flex items-center gap-1">
      <button
        type="button"
        onClick={onBack}
        aria-label="Back"
        className="touch-target inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted-foreground hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronLeft className="h-4 w-4" aria-hidden />
      </button>
      <h3 className="text-sm font-semibold text-foreground">{title}</h3>
    </div>
  );
}

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function ComposerOptions(props: ComposerOptionsProps) {
  const { page, onNavigate, onClose } = props;
  const contentRef = useRef<HTMLDivElement>(null);
  const toRoot = () => onNavigate({ id: "root" });

  // A pushed page replaces the control that pushed it, which takes focus with
  // it; move focus into the new page so keyboard and screen-reader users land
  // where they went. The first page is the Modal's to focus.
  const pageKey = page.id === "preset" ? `preset:${page.target}` : page.id;
  const firstPage = useRef(true);
  useEffect(() => {
    if (firstPage.current) {
      firstPage.current = false;
      return;
    }
    contentRef.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();
  }, [pageKey]);

  return (
    <ComposerSheet title="Chat options" onClose={onClose}>
      <div ref={contentRef} data-testid="composer-options" data-page={pageKey}>
        <OptionsPage {...props} toRoot={toRoot} />
      </div>
    </ComposerSheet>
  );
}

function OptionsPage(props: ComposerOptionsProps & { toRoot: () => void }) {
  const { page, toRoot, configure } = props;
  switch (page.id) {
    case "model":
      if (!configure || configure.status !== "ready" || configure.model === null) return <RootPage {...props} />;
      return <>{configure.renderSettings("model", toRoot)}</>;
    case "settings":
      if (!configure || configure.status !== "ready") return <RootPage {...props} />;
      return <>{configure.renderSettings(page.initial, toRoot)}</>;
    case "preset": {
      const row = props.presets.find((p) => p.target === page.target);
      if (!row) return <RootPage {...props} />;
      return <PresetPage row={row} onBack={toRoot} onSelect={(preset) => { props.onPresetChange(row.target, preset); toRoot(); }} />;
    }
    case "workflow":
      if (props.workflow.locked) return <RootPage {...props} />;
      return (
        <>
          <PageHeader title="Workflow" onBack={toRoot} />
          <ComposerWorkflowPicker
            value={props.workflow.value}
            onChange={(next) => {
              props.workflow.onChange(next);
              toRoot();
            }}
          />
        </>
      );
    case "commands":
      return (
        <>
          <PageHeader title="Commands" onBack={toRoot} />
          <div className={GROUP}>
            {props.commands.map((command) => (
              <button key={command.id} type="button" className={ROW} onClick={() => props.onCommand(command)}>
                {command.icon && <span className="flex shrink-0 items-center text-muted-foreground">{command.icon}</span>}
                <span className="min-w-0 flex-1">
                  <span className="block font-medium text-foreground">{command.title}</span>
                  <span className="block truncate text-xs text-muted-foreground">{command.description}</span>
                </span>
              </button>
            ))}
          </div>
        </>
      );
    case "account":
      return <AccountPage chatId={props.chatId} onBack={toRoot} />;
    case "root":
      return <RootPage {...props} />;
  }
}

function RootPage({
  onNavigate,
  attach,
  configure,
  presets,
  workflow,
  onPrompts,
  commands,
  chatId,
}: ComposerOptionsProps) {
  return (
    <div className="space-y-3 pb-2">
      {attach && (
        <div className={cn("grid gap-2", attach.camera ? "grid-cols-3" : "grid-cols-2")}>
          <AttachTile label="Photos" icon={<ImageIcon className="h-5 w-5" />} disabled={attach.disabled} onClick={() => attach.onPick("photos")} />
          {attach.camera && (
            <AttachTile label="Camera" icon={<Camera className="h-5 w-5" />} disabled={attach.disabled} onClick={() => attach.onPick("camera")} />
          )}
          <AttachTile label="Files" icon={<Paperclip className="h-5 w-5" />} disabled={attach.disabled} onClick={() => attach.onPick("files")} />
        </div>
      )}

      <div className={GROUP}>
        {configure?.status === "ready" && configure.model !== null && (
          <Row label="Model" value={configure.model} onClick={() => onNavigate({ id: "model" })} testId="options-model-row" />
        )}
        {presets.map((row) => (
          <Row
            key={row.target || "workflow"}
            label={row.target ? `${row.label} preset` : "Preset"}
            value={row.loading ? "Loading…" : row.value ?? "None"}
            onClick={() => onNavigate({ id: "preset", target: row.target })}
            disabled={row.loading}
          />
        ))}
        <WorkflowRow workflow={workflow} onOpen={() => onNavigate({ id: "workflow" })} />
        {configure && <SettingsRow configure={configure} onOpen={() => onNavigate({ id: "settings", initial: "main" })} />}
      </div>

      <div className={GROUP}>
        <Row label="Prompts" icon={<FileText className="h-4 w-4" />} onClick={onPrompts} />
        {commands.length > 0 && (
          <Row label="Commands" icon={<Slash className="h-4 w-4" />} onClick={() => onNavigate({ id: "commands" })} />
        )}
      </div>

      <AccountRow chatId={chatId} onOpen={() => onNavigate({ id: "account" })} />
    </div>
  );
}

function AttachTile({ label, icon, onClick, disabled }: { label: string; icon: ReactNode; onClick: () => void; disabled: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="flex min-h-[72px] flex-col items-center justify-center gap-1.5 rounded-xl border border-border/60 bg-background text-sm font-medium text-foreground hover:bg-foreground/5 active:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
    >
      <span className="text-muted-foreground">{icon}</span>
      {label}
    </button>
  );
}

function WorkflowRow({ workflow, onOpen }: { workflow: ComposerOptionsProps["workflow"]; onOpen: () => void }) {
  const { label } = useComposerWorkflows(workflow.value);
  if (workflow.locked) {
    // Fixed once the chat has started: say so, rather than offering a row that
    // opens onto choices it would refuse.
    return (
      <Row
        label="Workflow"
        value={
          <span className="flex flex-col items-end">
            <span className="truncate text-foreground">{label}</span>
            <span className="text-xs">Set when the chat began</span>
          </span>
        }
        testId="options-workflow-row"
      />
    );
  }
  return <Row label="Workflow" value={label} onClick={onOpen} disabled={workflow.disabled} testId="options-workflow-row" />;
}

function SettingsRow({ configure, onOpen }: { configure: NonNullable<ComposerOptionsProps["configure"]>; onOpen: () => void }) {
  // The workflow's settings come from its definition; say what state that is
  // in rather than showing an empty or silently missing row.
  switch (configure.status) {
    case "loading":
      return <div role="status" className={cn(ROW, "text-muted-foreground hover:bg-transparent")}>Loading workflow settings…</div>;
    case "error":
      return (
        <div role="alert" className={cn(ROW, "justify-between hover:bg-transparent")}>
          <span className="text-destructive-ink">Could not load workflow settings.</span>
          <button
            type="button"
            onClick={configure.onRetry}
            className="min-h-[44px] shrink-0 rounded-lg px-3 font-medium text-primary-ink hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Retry settings
          </button>
        </div>
      );
    case "empty":
      return <div className={cn(ROW, "text-muted-foreground hover:bg-transparent")}>This workflow has no configurable settings.</div>;
    case "ready":
      return (
        <Row
          label="All settings"
          onClick={onOpen}
          testId="options-settings-row"
          badge={
            configure.hasUnsetParams ? (
              <span className="flex items-center gap-1.5 text-xs font-normal text-destructive-ink">
                <span className="h-2 w-2 rounded-full bg-destructive" aria-hidden />
                Required inputs missing
              </span>
            ) : undefined
          }
        />
      );
  }
}

function PresetPage({ row, onBack, onSelect }: { row: PresetRow; onBack: () => void; onSelect: (preset: Preset | null) => void }) {
  // A preset set by an expression is replaced only on purpose, as in the
  // inline picker.
  const [replacing, setReplacing] = useState(false);
  const title = row.target ? `${row.label} preset` : "Preset";
  return (
    <>
      <PageHeader title={title} onBack={onBack} />
      {isCelTemplate(row.value) && !replacing ? (
        <div className="space-y-3 px-1 text-sm">
          <p className="font-medium text-foreground">Replace expression?</p>
          <p className="text-muted-foreground">
            This preset is set by an expression:
            <span className="mt-1 block break-all rounded border border-border/60 bg-background px-1.5 py-1 text-xs text-foreground">{row.value}</span>
            Replacing overwrites it with a fixed preset.
          </p>
          <div className="flex justify-end gap-2">
            <button type="button" onClick={onBack} className="min-h-[44px] rounded-lg border border-border px-3 hover:bg-foreground/5">Cancel</button>
            <button type="button" onClick={() => setReplacing(true)} className="min-h-[44px] rounded-lg bg-primary px-3 text-primary-foreground hover:bg-primary/90">Replace</button>
          </div>
        </div>
      ) : (
        <div className={GROUP}>
          <PresetList presets={row.presets} value={row.value} onSelect={onSelect} />
        </div>
      )}
    </>
  );
}

function AccountRow({ chatId, onOpen }: { chatId?: string; onOpen: () => void }) {
  const usage = useAccountUsageChip(chatId);
  if (!usage) return null;
  return (
    <div className={cn(GROUP, accountUsageTone(usage))}>
      <Row label="Account" icon={<Gauge className="h-4 w-4" />} value={accountUsageLabel(usage)} onClick={onOpen} testId="options-account-row" />
    </div>
  );
}

function AccountPage({ chatId, onBack }: { chatId?: string; onBack: () => void }) {
  const usage = useAccountUsageChip(chatId);
  return (
    <>
      <PageHeader title={usage ? `${usage.active.label} usage` : "Account usage"} onBack={onBack} />
      {usage ? (
        <div data-testid="account-usage-popover" className="space-y-2 px-1">
          <AccountUsageDetails usage={usage} compact />
        </div>
      ) : (
        <p className="px-1 text-sm text-muted-foreground">This account has room to spare.</p>
      )}
    </>
  );
}
