import { useState } from "react";
import { cn } from "../../lib/utils";
import { BarChart3, Building2, CircleUser, Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, ShieldCheck, Lock, FolderOpen, Globe, GitBranch, Bell, KeyRound, Github, CreditCard, Server, Layers, Zap, MessageSquare, Plug, ChevronRight } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { McpIcon } from "../icons/McpIcon";
import { hasControlPlane } from "../../services/controlPlane/config";
import { appBuild } from "../../lib/appBuild";
import { formatVersion } from "../../lib/releaseIdentity";
import { resolveAISettingsTab, type AISettingsTab, type SettingsSection, type SettingsTab } from "../../routeSchemas";

export type { SettingsSection };

interface SettingsNavigationProps {
  activeSection: SettingsSection;
  /** The URL's `?tab=`: picks which of the AI section's two entries is current. */
  activeTab?: SettingsTab;
  /** `tab` is set only by an entry that stands for some of a section's tabs (Models & tools). */
  onSectionChange: (section: SettingsSection, tab?: AISettingsTab) => void;
  isCollapsed?: boolean;
}

export interface SectionItem {
  id: SettingsSection;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  /**
   * The tabs of section `id` this entry stands for, when the section has more
   * than one entry. Settings → AI is one page with two entries: Personal
   * accounts (its providers tab) and Models & tools (its models and local
   * tabs). The first tab is where the entry goes; the default tab is left off
   * the URL, so /settings/ai and /settings/ai?tab=models stay the links they
   * were.
   */
  tabs?: readonly AISettingsTab[];
}

/** One sidebar entry: where it goes, in display order. */
export interface SettingsNavEntry {
  section: SettingsSection;
  tab?: AISettingsTab;
}

const DEFAULT_AI_TAB: AISettingsTab = "providers";

/** Unique per entry: a section, or a section and the tab it goes to. */
function navKey(entry: SettingsNavEntry): string {
  return entry.tab ? `${entry.section}:${entry.tab}` : entry.section;
}

function entryOf(item: SectionItem): SettingsNavEntry {
  const tab = item.tabs?.[0];
  return tab && tab !== DEFAULT_AI_TAB ? { section: item.id, tab } : { section: item.id };
}

/** Whether `item` is the current page: its section, and (for a tabbed entry) one of its tabs. */
function isItemActive(item: SectionItem, activeSection: SettingsSection, activeTab?: SettingsTab): boolean {
  if (item.id !== activeSection) return false;
  return !item.tabs || item.tabs.includes(resolveAISettingsTab(activeTab));
}

/** Where `item`'s entry goes: its section, and its tab when that is not the section's default. */
export function settingsItemEntry(item: SectionItem): SettingsNavEntry {
  return entryOf(item);
}

/** Whether `item` is the page on screen (its section, and one of its tabs). */
export function isSettingsItemCurrent(item: SectionItem, activeSection: SettingsSection, activeTab?: SettingsTab): boolean {
  return isItemActive(item, activeSection, activeTab);
}

export interface SectionGroup {
  label: string;
  items: SectionItem[];
  /** Folded behind its heading until opened, or until the active section is inside it. */
  collapsible?: boolean;
}

const ABOUT: SectionItem = { id: "about", label: "About", icon: Info };

/**
 * Six groups, each a real category. The everyday ones are open; Advanced is
 * folded away until someone goes looking (or lands on one of its pages).
 * Only grouping, order and labels differ from the old nav: every section
 * keeps its id and URL.
 *   Account              who you are and what you pay for;
 *   AI                   what your agents run on: the accounts you connect,
 *                        the ones your organization pays for, and which
 *                        models they use;
 *   Connections          GitHub, integrations and tools agents reach out through;
 *   Machines & projects  where agents work;
 *   Preferences          how this app looks and behaves;
 *   Advanced             prompts, presets, approvals, branches, tokens, the built-in browser.
 * About is not a group: it is the footer link.
 */
const sectionGroups: SectionGroup[] = [
  {
    label: "Account",
    items: [
      { id: "account", label: "Account", icon: User },
      // Cloud-only: both need a control-plane backend, and a self-hosted
      // deployment has no organization to grant permissions within.
      ...(hasControlPlane
        ? [
            { id: "billing" as const, label: "Billing & usage", icon: CreditCard },
            { id: "member-permissions" as const, label: "Members", icon: Shield },
            { id: "policies" as const, label: "Policies", icon: ShieldCheck },
            { id: "org-ai-usage" as const, label: "AI usage", icon: BarChart3 },
          ]
        : []),
      { id: "privacy", label: "Privacy", icon: Lock },
    ],
  },
  {
    label: "AI",
    items: [
      // Two entries for one page: Settings → AI's providers tab, and its
      // models and local tabs. Whose account pays is unchanged by the grouping:
      // each entry opens the same page and permission checks as before.
      { id: "ai", label: "Personal accounts", icon: CircleUser, tabs: ["providers"] },
      // Cloud-only: an organization exists only with a control plane. Members
      // read it; the page itself decides who may manage it.
      ...(hasControlPlane ? [{ id: "org-ai-accounts" as const, label: "Organization accounts", icon: Building2 }] : []),
      { id: "ai", label: "Models & tools", icon: Sparkles, tabs: ["models", "local"] },
    ],
  },
  {
    label: "Connections",
    items: [
      // The connections automations and workflow steps act through (Slack,
      // Gmail, Twilio, HTTP, GitHub's integration), to find, test or revoke.
      { id: "integrations", label: "Integrations", icon: Plug },
      { id: "git-connections", label: "GitHub", icon: Github },
      // Cloud-only: the GitHub App installations linked to the organization.
      // Members read them; the page decides who may link.
      ...(hasControlPlane ? [{ id: "org-github" as const, label: "Organization GitHub", icon: Building2 }] : []),
      { id: "mcp", label: "Tools (MCP)", icon: McpIcon },
    ],
  },
  {
    label: "Sandboxes & projects",
    items: [
      // Not gated on hasControlPlane: self-hosted machines, and the outside
      // AI apps granted access to each (its Access section), exist without
      // one — and Machines is the only place such a grant can be revoked.
      { id: "machines", label: "Agent sandboxes", icon: Server },
      { id: "projects", label: "Projects", icon: FolderOpen },
    ],
  },
  {
    label: "Preferences",
    items: [
      { id: "appearance", label: "Appearance", icon: Monitor },
      { id: "chat-editor", label: "Chat & editor", icon: MessageSquare },
      { id: "notifications", label: "Notifications", icon: Bell },
      { id: "shortcuts", label: "Keyboard shortcuts", icon: Keyboard },
    ],
  },
  {
    label: "Advanced",
    collapsible: true,
    items: [
      { id: "prompts", label: "Prompts", icon: List },
      { id: "presets", label: "Presets", icon: Layers },
      { id: "automations", label: "Approve agent changes", icon: Zap },
      { id: "branches", label: "Workspaces", icon: GitBranch },
      // Tokens exist to let a self-hosted machine or headless daemon connect.
      { id: "tokens", label: "Access tokens", icon: KeyRound },
      // Reliant's own browser is app behaviour, not something agents run on.
      { id: "browser", label: "Built-in browser", icon: Globe },
      { id: "developer", label: "Developer", icon: Code },
    ],
  },
];

/**
 * Developer settings (Restart Backend, experimental flags)
 * are for people building Reliant: a Vite dev server, or a runtime config
 * that says it is a dev build (an unpackaged Electron app sets isDev). A
 * production web build is neither, so its users never see the section —
 * SettingsViewerTab sends a deep link to /settings/developer to the first
 * visible section instead.
 */
export function showDeveloperSettings(): boolean {
  return import.meta.env.DEV || Boolean(window.RELIANT_CONFIG?.isDev);
}

/**
 * Settings sections whose page belongs to another area of the shell's nav.
 * Agent sandboxes is Agents › Sandboxes too (`sandboxesPath()` is
 * `/settings/machines`), and on its pages the nav column is Agents', not
 * Settings'. Settings still lists the section, as a link there.
 *
 * One page has one nav home, so Settings neither lights up on these pages nor
 * reopens them: bare /settings restoring one landed on a page with no
 * Settings sections in the nav, and every later Settings click landed there
 * again (settingsDestination.ts). The nav tests check this list against the
 * nav config, so a section that moves under another area must be added here.
 */
export const SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME: readonly SettingsSection[] = ["machines"];

/** Whether `section`'s page is in another nav area, not Settings' (see SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME). */
export function settingsSectionHasOtherNavHome(section: SettingsSection): boolean {
  return SETTINGS_SECTIONS_WITH_OTHER_NAV_HOME.includes(section);
}

/** True if the item should appear in the sidebar for the current build. */
function isItemVisible(item: SectionItem): boolean {
  if (item.id === "developer") return showDeveloperSettings();
  return true;
}

/**
 * The sections as the shell's Settings drill-in lists them (U6c: Settings
 * replaces the left nav instead of opening a second column): each group's
 * visible sections, then About.
 */
export function settingsNavGroups(): { groups: SectionGroup[]; about: SectionItem } {
  return {
    groups: sectionGroups
      .map((group) => ({ ...group, items: group.items.filter(isItemVisible) }))
      .filter((group) => group.items.length > 0),
    about: ABOUT,
  };
}

/** Sidebar entries in display order (About last, as the footer); used for keyboard nav. */
export function getVisibleSettingsNavEntries(): SettingsNavEntry[] {
  return [...sectionGroups.flatMap((group) => group.items.filter(isItemVisible).map(entryOf)), { section: ABOUT.id }];
}

/** The index of the current page's entry in {@link getVisibleSettingsNavEntries}, or -1. */
export function activeSettingsNavEntryIndex(entries: SettingsNavEntry[], section: SettingsSection, tab?: SettingsTab): number {
  const matches = entries.flatMap((entry, index) => (entry.section === section ? [index] : []));
  if (matches.length <= 1) return matches[0] ?? -1;
  const item = sectionGroups.flatMap((group) => group.items.filter(isItemVisible)).find((i) => isItemActive(i, section, tab));
  const wanted = item ? entryOf(item) : undefined;
  return matches.find((index) => entries[index].tab === wanted?.tab) ?? matches[0];
}

/** The button that stands for `entry`, as {@link SettingsNavigation} marks it. */
export function settingsNavEntrySelector(entry: SettingsNavEntry): string {
  return `button[data-nav-key="${navKey(entry)}"]`;
}

/** Section IDs in sidebar display order, each once (About last, as the footer). */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  return [...new Set(getVisibleSettingsNavEntries().map((entry) => entry.section))];
}

/** Whether `section` sits in a folded-away group (Advanced). */
export function isInCollapsibleGroup(section: SettingsSection): boolean {
  return sectionGroups.some((group) => group.collapsible && group.items.some((item) => item.id === section && isItemVisible(item)));
}

export function SettingsNavigation({
  activeSection,
  activeTab,
  onSectionChange,
  isCollapsed = false,
}: SettingsNavigationProps) {
  // The user's own open of Advanced, good only for the section it was made
  // on. While the active section is inside Advanced the group stays open and
  // its heading cannot fold it: folding would hide the item that is current.
  const [toggled, setToggled] = useState<{ section: SettingsSection; open: boolean } | null>(null);
  const activeInAdvanced = isInCollapsibleGroup(activeSection);
  const advancedOpen = activeInAdvanced || (toggled?.section === activeSection && toggled.open);

  const renderItem = (section: SectionItem) => {
    const isActive = isItemActive(section, activeSection, activeTab);
    const entry = entryOf(section);
    const key = navKey(entry);
    const item = (
      <button
        key={key}
        onClick={() => (entry.tab ? onSectionChange(entry.section, entry.tab) : onSectionChange(entry.section))}
        aria-label={section.label}
        aria-current={isActive ? "page" : undefined}
        data-section-id={section.id}
        data-nav-key={key}
        className={cn(
          "w-full cursor-pointer rounded-md border-l-2 px-3 py-1.5 text-sm transition-all",
          isCollapsed ? "flex items-center justify-center px-2" : "text-left",
          isActive
            ? "border-primary bg-primary/10 text-primary-ink font-medium"
            : "border-transparent text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
        )}
      >
        <div className={cn("flex items-center", isCollapsed ? "justify-center" : "gap-2.5")}>
          <section.icon className="h-4 w-4 flex-shrink-0" />
          {!isCollapsed && <div className="font-medium">{section.label}</div>}
        </div>
      </button>
    );
    return isCollapsed ? (
      <Tooltip key={key} content={section.label} side="right" triggerClassName="block w-full">
        {item}
      </Tooltip>
    ) : (
      item
    );
  };

  const build = appBuild();
  const version = formatVersion(build?.version, build?.commit);
  const footerLabel = version ? `Reliant ${version} · About` : "About";

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className={cn("min-h-0 flex-1 overflow-y-auto px-2 pb-4 pt-1", isCollapsed && "pb-2")}>
        <nav className="space-y-1">
          {sectionGroups.map((group) => {
            const visibleItems = group.items.filter(isItemVisible);
            if (visibleItems.length === 0) return null;
            // The narrow rail has no room for a heading to open, so it lists everything.
            const folded = group.collapsible && !isCollapsed && !advancedOpen;
            const listId = `settings-group-${group.label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;

            return (
              <div key={group.label} data-testid={`settings-group-${group.label}`}>
                {!isCollapsed &&
                  (group.collapsible ? (
                    <button
                      type="button"
                      aria-expanded={!folded}
                      aria-controls={listId}
                      aria-disabled={activeInAdvanced || undefined}
                      onClick={() => {
                        if (!activeInAdvanced) setToggled({ section: activeSection, open: !advancedOpen });
                      }}
                      className="flex w-full cursor-pointer items-center aria-disabled:cursor-default gap-1 px-3 pb-1 pt-3 text-left text-xs font-medium text-muted-foreground hover:text-foreground"
                    >
                      <ChevronRight className={cn("h-3 w-3 transition-transform", !folded && "rotate-90")} aria-hidden="true" />
                      {group.label}
                    </button>
                  ) : (
                    <div className="px-3 pb-1 pt-3 text-xs font-medium text-muted-foreground">{group.label}</div>
                  ))}
                {!folded && (
                  <div id={listId} className="space-y-0.5">
                    {visibleItems.map(renderItem)}
                  </div>
                )}
              </div>
            );
          })}
        </nav>
      </div>
      <div className={cn("shrink-0 border-t border-border/60 px-2 py-2", isCollapsed && "flex justify-center")}>
        {isCollapsed ? (
          renderItem(ABOUT)
        ) : (
          // One line, whatever the version: a long one (a `.dirty` or sha
          // build) is truncated, and the tooltip shows it whole.
          <Tooltip content={footerLabel} side="right" triggerClassName="block w-full">
            <button
              onClick={() => onSectionChange(ABOUT.id)}
              aria-label={ABOUT.label}
              aria-current={activeSection === ABOUT.id ? "page" : undefined}
              data-section-id={ABOUT.id}
              data-nav-key={ABOUT.id}
              className={cn(
                "flex w-full cursor-pointer items-center gap-2.5 rounded-md border-l-2 px-3 py-1.5 text-left text-xs transition-all",
                activeSection === ABOUT.id
                  ? "border-primary bg-primary/10 text-primary-ink font-medium"
                  : "border-transparent text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
              )}
            >
              <ABOUT.icon className="h-4 w-4 flex-shrink-0" />
              <span data-testid="settings-footer-label" className="min-w-0 truncate">
                {footerLabel}
              </span>
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  );
}
