// Copyright (c) 2025 Reliant Labs

/**
 * The side nav's building blocks (restyle design §11.2). Version (a) of the
 * nav composes them under today's global header; U6 recomposes them at full
 * height. Each one owns its classes, so U6's sidebar is a rearrangement of
 * these, not a restyle.
 *
 * One surface (`bg-background`), whitespace between blocks rather than
 * borders, rows at `--row-h`, hover and selection as an ink wash
 * (`bg-foreground/5`, `/[0.08]`), and status as a dot, never a chip or a
 * tinted row (web-styling: "Status colour is a dot, an icon or a count").
 */

import type React from "react";
import type { ReactNode } from "react";
import { ChevronDown, ChevronRight, ChevronsUpDown, GitBranch, Loader2 } from "lucide-react";

import { isForgeShell } from "../../../lib/shell";
import { cn } from "../../../lib/utils";
import { ForgeMark } from "../../ForgeShell/ForgeBrand";
import { BrandMark } from "../../icons/BrandMark";
import { ProjectMark } from "../ProjectMark";
import { CHAT_ROW_DOT_CLASS, type ChatRowChip } from "../chatRowStatus";

/** A 24px icon button in a row's trailing slot (New chat in a project, More options). */
export const SIDEBAR_ICON_BUTTON =
  "inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const FOCUS_INSET = "focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring";

// --- The org row ----------------------------------------------------------

/** The top row: 48px, the org switcher (or the product's name) in it. */
export function SidebarOrgRow({ children }: { children: ReactNode }) {
  return <div className="flex h-[var(--shell-bar-h)] shrink-0 items-center px-2">{children}</div>;
}

/**
 * The org row's control: a button when it opens a menu, a plain row when it does not.
 * A nav row's height (`--row-h`), like Find and the rows under it, so the narrow
 * window's and the phone's drawer make it the same 44px touch target as the rest.
 */
export const SIDEBAR_ORG_TRIGGER =
  "flex h-[var(--row-h)] min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left text-ui text-foreground transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * The brand's mark at the nav's top: Forge's on the forge host (and the demo),
 * Reliant's everywhere else. One shell, two brands (U6): only the mark moves.
 */
export function ShellBrandMark() {
  return isForgeShell() ? <ForgeMark className="size-5" /> : <BrandMark className="size-5 shrink-0" />;
}

/** What the org row shows: the mark, the name, and the switcher's chevron when there is a menu. */
export function SidebarOrgLabel({
  name,
  menu = false,
  nameTestId,
}: {
  name: string;
  menu?: boolean;
  nameTestId?: string;
}) {
  return (
    <>
      <ShellBrandMark />
      <span className="min-w-0 flex-1 truncate font-semibold" data-testid={nameTestId}>
        {name}
      </span>
      {menu && <ChevronsUpDown className="ml-auto size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
    </>
  );
}

// --- The project row ------------------------------------------------------

/** The row under the org row (and Find) naming the project the nav is about. */
export function SidebarProjectRow({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <div className="flex shrink-0 px-2 pb-1 pt-1" data-testid={testId}>
      {children}
    </div>
  );
}

/** The project row's control: the org row's, at the row's width. */
export const SIDEBAR_PROJECT_TRIGGER = cn(SIDEBAR_ORG_TRIGGER, "w-full gap-2 px-1.5");

/**
 * A project row that opens nothing (the public demo's one sample project): the
 * project switcher's mark and name (ProjectSwitcher.tsx), without its menu,
 * chevron or hover wash.
 */
export function SidebarProjectLabel({ name, testId }: { name: string; testId?: string }) {
  return (
    <SidebarProjectRow testId={testId}>
      <div className={cn(SIDEBAR_PROJECT_TRIGGER, "hover:bg-transparent")}>
        <ProjectMark name={name} className="size-5 text-meta" />
        <span className="min-w-0 truncate font-semibold">{name}</span>
      </div>
    </SidebarProjectRow>
  );
}

// --- The primary action ---------------------------------------------------

/** The one filled button in the nav: New chat. */
export function SidebarPrimaryAction({
  icon,
  label,
  shortcut,
  onClick,
  href,
  testId,
}: {
  icon: ReactNode;
  label: string;
  /** The action's shortcut, formatted (useShortcutLabel). */
  shortcut?: string;
  onClick: () => void;
  /** The page it opens. Given, it is a link (a modified click opens a new tab) and a plain click runs `onClick`. */
  href?: string;
  testId?: string;
}) {
  const shared = {
    "data-testid": testId,
    // The label alone names the action; the key cap is a visual hint.
    "aria-label": label,
    className: cn(
      // --primary-hover is a real colour per scheme with no Tailwind alias.
      "no-color mx-2 flex h-8 shrink-0 items-center gap-2 rounded-md bg-primary px-2 text-left text-ui font-medium text-primary-foreground transition-colors hover:bg-[hsl(var(--primary-hover))] hover:text-primary-foreground",
      "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    ),
  };
  const content = (
    <>
      <span className="flex size-4 shrink-0 items-center justify-center" aria-hidden="true">
        {icon}
      </span>
      <span className="min-w-0 truncate">{label}</span>
      {shortcut && (
        <kbd aria-hidden="true" className="ml-auto shrink-0 font-sans text-meta text-primary-foreground">
          {shortcut}
        </kbd>
      )}
    </>
  );
  return href ? (
    <a {...shared} href={href} onClick={routedClick(onClick)}>
      {content}
    </a>
  ) : (
    <button type="button" {...shared} onClick={onClick}>
      {content}
    </button>
  );
}

// --- Rows -----------------------------------------------------------------

/**
 * A link's click, kept in the app: a plain click runs `onNavigate`, and a
 * modified one (new tab, new window) is left to the browser.
 */
function routedClick(onNavigate: (() => void) | undefined) {
  return (event: React.MouseEvent<HTMLAnchorElement>) => {
    if (!onNavigate || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
    event.preventDefault();
    onNavigate();
  };
}

export interface SidebarRowProps {
  icon: ReactNode;
  label: string;
  onClick?: () => void;
  /**
   * The page the row opens. Given, the row is a link (a modified click opens
   * it in a new tab) and a plain click runs `onClick`.
   */
  href?: string;
  /** Quiet trailing text inside the row: a count, a shortcut, a dot. */
  trail?: ReactNode;
  /** A control of its own after the row (it cannot nest in the row's button). */
  action?: ReactNode;
  current?: boolean;
  /** For a row that opens and closes something below it. */
  expanded?: boolean;
  /**
   * The button's accessible name; defaults to the label. null names it by
   * its content instead (a row whose children say more, like Inbox's count).
   */
  ariaLabel?: string | null;
  testId?: string;
  onboardingId?: string;
  className?: string;
  /** More content inside the button, after the trail (a live region). */
  children?: ReactNode;
}

/** A nav row: Inbox, Apps, Workflows, Search, Settings, More projects. */
export function SidebarRow({
  icon,
  label,
  onClick,
  href,
  trail,
  action,
  current = false,
  expanded,
  ariaLabel,
  testId,
  onboardingId,
  className,
  children,
}: SidebarRowProps) {
  const content = (
    <>
      <span
        className={cn("flex size-4 shrink-0 items-center justify-center", current ? "text-foreground" : "text-muted-foreground")}
        aria-hidden="true"
      >
        {icon}
      </span>
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {trail && <span className="ml-auto flex shrink-0 items-center gap-1.5 text-meta text-muted-foreground">{trail}</span>}
      {children}
    </>
  );
  const shared = {
    "aria-current": current ? ("page" as const) : undefined,
    "aria-expanded": expanded,
    "aria-label": ariaLabel === null ? undefined : (ariaLabel ?? label),
    "data-testid": testId,
    "data-onboarding": onboardingId,
  };
  const target = "flex h-full min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left";
  return (
    <div
      className={cn(
        "flex h-[var(--row-h)] min-w-0 items-center rounded-md text-ui text-foreground transition-colors hover:bg-foreground/5",
        current && "bg-foreground/[0.08] font-medium",
        className,
      )}
    >
      {href ? (
        <a {...shared} href={href} onClick={routedClick(onClick)} className={cn(target, "no-color text-foreground", FOCUS_INSET)}>
          {content}
        </a>
      ) : (
        <button
          {...shared}
          type="button"
          onClick={onClick}
          disabled={!onClick}
          className={cn(target, "disabled:cursor-default disabled:opacity-50", FOCUS_INSET)}
        >
          {content}
        </button>
      )}
      {action && <span className="flex shrink-0 items-center pr-1">{action}</span>}
    </div>
  );
}

// --- Nested rows ----------------------------------------------------------

/**
 * How far a nested row's text sits in. Depth 0 is a top-level row (the
 * project shell's app sections, drawn like the forge console's). Depth 1 lines its icon up under the
 * parent's label (an app's sections under the app); depth 2 lines its label
 * up with depth 1's (Errors under Issues); depth 3 steps in once more (a
 * starred dashboard under Dashboards).
 */
const SUB_INDENT = ["pl-2", "pl-8", "pl-14", "pl-16"] as const;

export interface SidebarSubRowProps {
  label: string;
  /** Depth under the top-level row, 1–3 (0: a top-level row itself). */
  depth?: 0 | 1 | 2 | 3;
  icon?: ReactNode;
  /** The page the row opens; omitted, the row is a button (a group that opens in place). */
  href?: string;
  onClick?: () => void;
  current?: boolean;
  /** For a group: whether its rows show. Draws the chevron. */
  expanded?: boolean;
  /** Quiet trailing content: a count, a mark. */
  trail?: ReactNode;
  /** The accessible name, when the trail is only a hint (a shortcut); defaults to the content. */
  ariaLabel?: string;
  testId?: string;
}

/** A row nested under a nav row: an app's console sections, the Workflows area's tabs. */
export function SidebarSubRow({
  label,
  depth = 1,
  icon,
  href,
  onClick,
  current = false,
  expanded,
  trail,
  ariaLabel,
  testId,
}: SidebarSubRowProps) {
  const content = (
    <>
      {icon && (
        <span
          className={cn("flex size-4 shrink-0 items-center justify-center", current ? "text-foreground" : "text-muted-foreground")}
          aria-hidden="true"
        >
          {icon}
        </span>
      )}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {trail && <span className="ml-auto flex shrink-0 items-center gap-1.5 text-meta text-muted-foreground">{trail}</span>}
      {expanded !== undefined && (
        <ChevronRight
          className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", expanded && "rotate-90")}
          aria-hidden="true"
        />
      )}
    </>
  );
  const target = cn(
    "flex h-[var(--row-h)] w-full min-w-0 items-center gap-2 rounded-md pr-2 text-left text-ui transition-colors hover:bg-foreground/5 hover:text-foreground",
    SUB_INDENT[depth],
    current ? "bg-foreground/[0.08] font-medium text-foreground" : depth === 0 ? "text-foreground" : "text-foreground/75",
    FOCUS_INSET,
  );
  return href ? (
    <a
      href={href}
      onClick={routedClick(onClick)}
      aria-current={current ? "page" : undefined}
      aria-expanded={expanded}
      aria-label={ariaLabel}
      data-testid={testId}
      className={cn(target, "no-color")}
    >
      {content}
    </a>
  ) : (
    <button type="button" onClick={onClick} aria-expanded={expanded} aria-label={ariaLabel} data-testid={testId} className={target}>
      {content}
    </button>
  );
}

/** A quiet label over a run of nested rows ("Configure" in an app's sections). */
export function SidebarSubLabel({ children, depth = 1 }: { children: ReactNode; depth?: 0 | 1 | 2 }) {
  return (
    <div className={cn("mt-3 flex h-7 items-center text-meta font-medium text-muted-foreground", SUB_INDENT[depth])}>
      {children}
    </div>
  );
}

/** The footer block under the nav: a hairline above it, where the nav scrolls under. */
export function SidebarFooter({ children }: { children: ReactNode }) {
  // The hairline is the edge the scrolling nav above passes under.
  return <div className="shrink-0 space-y-0.5 border-t border-border-subtle p-2">{children}</div>;
}

// --- The Chats heading ----------------------------------------------------

/** A heading control that shows on hover or focus, and stays while its menu is open. */
export const SIDEBAR_HEADING_HOVER_ACTION =
  "opacity-0 group-hover/heading:opacity-100 group-focus-within/heading:opacity-100 focus-visible:opacity-100 aria-expanded:opacity-100";

/** "Chats" over the list, with its controls at the right. */
export function SidebarHeading({
  children,
  actions,
  onboardingId,
  testId,
}: {
  children: ReactNode;
  actions?: ReactNode;
  onboardingId?: string;
  testId?: string;
}) {
  return (
    <div
      className="group/heading mt-6 flex h-7 shrink-0 items-center gap-1 pl-4 pr-3 text-meta text-muted-foreground"
      data-onboarding={onboardingId}
      data-testid={testId}
    >
      <div className="flex min-w-0 flex-1 items-center gap-1">{children}</div>
      {actions && <div className="ml-auto flex shrink-0 items-center gap-0.5">{actions}</div>}
    </div>
  );
}

// --- Groups ---------------------------------------------------------------

/**
 * A project's header in the grouped list (or a workspace's, when the list is
 * filtered to one project): the chevron, a mark, the name, what stays in the
 * trail (pinned, needs you), and hover-only actions. Sticky, so the project
 * a chat belongs to stays in view while its chats scroll under it.
 */
export function ProjectGroupHeader({
  name,
  mark,
  expanded,
  onToggle,
  trail,
  actions,
  muted = false,
  testId,
  onContextMenu,
}: {
  name: string;
  mark: ReactNode;
  expanded: boolean;
  onToggle: () => void;
  /** At rest: a pin, the needs-you dot. */
  trail?: ReactNode;
  /** On hover or focus only: New chat here, the project's menu. */
  actions?: ReactNode;
  /** The name in muted ink (a hidden project, listed anyway). */
  muted?: boolean;
  testId?: string;
  onContextMenu?: (e: React.MouseEvent) => void;
}) {
  return (
    <div
      className="group/proj sticky top-0 z-10 flex h-8 items-center rounded-md bg-background text-ui font-medium text-foreground"
      data-testid={testId}
      onContextMenu={onContextMenu}
    >
      {/* The wash is a layer, so the sticky header stays opaque over the rows under it. */}
      <div className="pointer-events-none absolute inset-0 rounded-md transition-colors group-hover/proj:bg-foreground/5" />
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className={cn("relative flex h-full min-w-0 flex-1 items-center gap-2 rounded-md pl-2 pr-1 text-left", FOCUS_INSET)}
      >
        <ChevronDown
          className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform", !expanded && "-rotate-90")}
          aria-hidden="true"
        />
        {mark}
        <span className={cn("min-w-0 truncate", muted && "text-muted-foreground")} title={name}>
          {name}
        </span>
        {trail && <span className="ml-auto flex shrink-0 items-center gap-1.5 pl-1">{trail}</span>}
      </button>
      {actions && (
        <span
          className={cn(
            "relative hidden shrink-0 items-center pr-1",
            "group-hover/proj:flex group-focus-within/proj:flex has-[[aria-expanded=true]]:flex",
          )}
        >
          {actions}
        </span>
      )}
    </div>
  );
}

/** A workspace (branch) inside a project: a quiet label, not a second heading. */
export function WorkspaceSubheading({
  branch,
  expanded,
  onToggle,
  actions,
  onContextMenu,
}: {
  branch: string;
  expanded: boolean;
  onToggle: () => void;
  /** On hover or focus only: New chat here, Archive. */
  actions?: ReactNode;
  onContextMenu?: (e: React.MouseEvent) => void;
}) {
  return (
    <div
      className="group/ws flex h-7 items-center gap-1 rounded-md pl-8 pr-1 text-meta text-muted-foreground"
      onContextMenu={onContextMenu}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className={cn("flex h-full min-w-0 flex-1 items-center gap-1.5 rounded-md text-left hover:text-foreground", FOCUS_INSET)}
      >
        <GitBranch className="size-3 shrink-0" aria-hidden="true" />
        <span className="min-w-0 truncate" title={branch}>
          {branch}
        </span>
        {/* A folded workspace says so at rest; an open one only on hover. */}
        <ChevronRight
          className={cn(
            "size-3 shrink-0 transition-transform",
            expanded ? "hidden rotate-90 group-hover/ws:block group-focus-within/ws:block" : "block",
          )}
          aria-hidden="true"
        />
      </button>
      {actions && (
        <span className="hidden shrink-0 items-center group-hover/ws:flex group-focus-within/ws:flex">{actions}</span>
      )}
    </div>
  );
}

/**
 * A section of the chat list (U6d, owner): "project · branch" as one small
 * heading at the list's left edge, its chats under it at the same edge. The
 * project and the workspace read at a glance without a deep indent.
 */
export function ChatSectionHeading({
  project,
  branch,
  expanded,
  onToggle,
  trail,
  actions,
  testId,
  onContextMenu,
}: {
  project?: string;
  branch: string;
  expanded: boolean;
  onToggle: () => void;
  /** At rest: a pin, the needs-you dot. */
  trail?: ReactNode;
  /** On hover or focus only: New chat here, Archive, the project's menu. */
  actions?: ReactNode;
  testId?: string;
  onContextMenu?: (e: React.MouseEvent) => void;
}) {
  return (
    <div
      className="group/ws sticky top-0 z-10 mt-2 flex h-7 items-center gap-1 rounded-md bg-background pl-2 pr-1 text-meta text-muted-foreground first:mt-0"
      data-testid={testId}
      onContextMenu={onContextMenu}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className={cn("flex h-full min-w-0 flex-1 items-center gap-1 rounded-md text-left hover:text-foreground", FOCUS_INSET)}
      >
        {project && (
          <>
            <span className="min-w-0 max-w-[60%] shrink-0 truncate font-medium text-foreground/80" title={project}>
              {project}
            </span>
            <span aria-hidden="true">·</span>
          </>
        )}
        <span className="min-w-0 shrink-[2] truncate" title={branch}>
          {branch}
        </span>
        <ChevronRight
          className={cn(
            "size-3 shrink-0 transition-transform",
            expanded ? "hidden rotate-90 group-hover/ws:block group-focus-within/ws:block" : "block",
          )}
          aria-hidden="true"
        />
        {trail && <span className="ml-auto flex shrink-0 items-center gap-1.5 pl-1">{trail}</span>}
      </button>
      {actions && (
        <span className="hidden shrink-0 items-center group-hover/ws:flex group-focus-within/ws:flex has-[[aria-expanded=true]]:flex">{actions}</span>
      )}
    </div>
  );
}

// --- Chat rows ------------------------------------------------------------

export interface ChatRowProps {
  chatId: string;
  title: string;
  /** The title's native tooltip: the full title and when it last changed. */
  tooltip?: string;
  current?: boolean;
  unread?: boolean;
  /**
   * Under a section heading. Rows sit at the list's one left edge either way
   * (U6d, owner: no deep indent); kept so callers say where a row is.
   */
  inProject?: boolean;
  /** The 16px state slot at the left (ChatStateSlot). */
  slot?: ReactNode;
  /** After the title, in meta: where the chat is, in the flat list. */
  suffix?: ReactNode;
  /** A quiet glyph after the title (the automation origin). */
  glyph?: ReactNode;
  /** Hover-only controls at the right (More options). */
  hoverActions?: ReactNode;
  /** Replaces the row's button while the title is being edited. */
  editor?: ReactNode;
  /** The one row in the tab order (the open chat, else the first). */
  isTabStop?: boolean;
  onClick: () => void;
  onContextMenu?: (e: React.MouseEvent) => void;
}

/**
 * A chat in the list. The row's primary action is a real button: focusable,
 * announced with its state, and Up/Down move between rows (the list's keydown
 * handler reads `data-chat-row`). Only one row is in the tab order.
 */
export function ChatRow({
  chatId,
  title,
  tooltip,
  current = false,
  unread = false,
  inProject: _inProject = false,
  slot,
  suffix,
  glyph,
  hoverActions,
  editor,
  isTabStop = false,
  onClick,
  onContextMenu,
}: ChatRowProps) {
  const indent = "pl-2";
  return (
    <div
      data-chat-id={chatId}
      className={cn(
        "group/chat relative flex h-[var(--row-h)] w-full min-w-0 items-center rounded-md pr-1 text-ui transition-colors hover:bg-foreground/5",
        current && "bg-foreground/[0.08]",
      )}
      onContextMenu={onContextMenu}
    >
      {editor ? (
        <div className={cn("flex min-w-0 flex-1 items-center gap-2", indent)}>
          <span className="w-4 shrink-0" />
          {editor}
        </div>
      ) : (
        <button
          type="button"
          data-chat-row
          tabIndex={isTabStop ? 0 : -1}
          aria-current={current ? "true" : undefined}
          onClick={onClick}
          className={cn("flex h-full min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-md text-left", indent, FOCUS_INSET)}
        >
          <span className="relative flex w-4 shrink-0 items-center justify-center">{slot}</span>
          <span className="flex min-w-0 flex-1 items-baseline gap-1">
            <span
              className={cn(
                "min-w-0 truncate transition-colors group-hover/chat:text-foreground",
                current || unread ? "text-foreground" : "text-foreground/80",
                unread && "font-medium",
              )}
              title={tooltip ?? title}
            >
              {title}
            </span>
            {suffix && <span className="min-w-0 shrink-[2] truncate text-meta text-muted-foreground">{suffix}</span>}
          </span>
          {glyph}
        </button>
      )}
      {hoverActions && (
        <div className="hidden shrink-0 items-center group-hover/chat:flex group-focus-within/chat:flex has-[[aria-expanded=true]]:flex">
          {hoverActions}
        </div>
      )}
    </div>
  );
}

/**
 * A chat's state in the row's 16px slot (§11.4): a spinner while it runs, a
 * warning dot when it needs you (ringed by a spinner while its other agents
 * still run), a destructive dot when it failed, a muted one when paused.
 * Each carries its words as its accessible name and its tooltip.
 */
export function ChatStateSlot({
  chatId,
  state,
  activityState,
  runningBeside = false,
}: {
  chatId: string;
  state: ChatRowChip;
  activityState: string;
  /** Agents still running beside the question. */
  runningBeside?: boolean;
}) {
  const label = state.detail ?? state.label;
  if (state.tone === "running") {
    return (
      <Loader2
        className="size-3 shrink-0 animate-spin text-primary-ink motion-reduce:animate-none"
        data-testid={`chat-activity-dot-${chatId}`}
        data-activity-state={activityState}
        data-tone="running"
        role="img"
        aria-label={label}
      >
        <title>{label}</title>
      </Loader2>
    );
  }
  const attention = state.tone === "attention";
  return (
    <span
      className="relative flex size-3.5 items-center justify-center"
      data-testid={attention ? `chat-needs-input-${chatId}` : `chat-activity-dot-${chatId}`}
      data-activity-state={activityState}
      data-tone={state.tone}
    >
      <span
        role="img"
        aria-label={label}
        title={label}
        className={cn("size-2 rounded-full", CHAT_ROW_DOT_CLASS[state.tone])}
      />
      {runningBeside && (
        <span
          aria-hidden="true"
          data-testid={`chat-needs-input-running-${chatId}`}
          className="absolute inset-0 animate-spin rounded-full border-[1.5px] border-transparent border-t-primary-ink motion-reduce:animate-none"
        />
      )}
    </span>
  );
}
