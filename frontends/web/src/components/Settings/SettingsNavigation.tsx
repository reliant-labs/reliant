import { useState } from "react";
import { cn } from "../../lib/utils";
import { Bot, Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, ShieldCheck, Lock, FolderOpen, Globe, GitBranch, Bell, KeyRound, Github, CreditCard, Server, Layers, Zap, MessageSquare, Plug, ChevronRight } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { McpIcon } from "../icons/McpIcon";
import { hasControlPlane } from "../../services/controlPlane/config";
import { appBuild } from "../../lib/appBuild";
import { formatVersion } from "../../lib/releaseIdentity";
import type { SettingsSection } from "../../routeSchemas";

export type { SettingsSection };

interface SettingsNavigationProps {
  activeSection: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
  isCollapsed?: boolean;
}

interface SectionItem {
  id: SettingsSection;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
}

interface SectionGroup {
  label: string;
  items: SectionItem[];
  /** Folded behind its heading until opened, or until the active section is inside it. */
  collapsible?: boolean;
}

const ABOUT: SectionItem = { id: "about", label: "About", icon: Info };

/**
 * Six groups, each a real category. The everyday ones are open; Advanced is
 * folded away until someone goes looking (or lands on one of its pages).
 * Only grouping, order and two labels differ from the old nav: every section
 * keeps its id and URL.
 *   Account              who you are and what you pay for;
 *   AI                   what your agents run on;
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
            { id: "org-ai-accounts" as const, label: "AI accounts", icon: Bot },
          ]
        : []),
      { id: "privacy", label: "Privacy", icon: Lock },
    ],
  },
  {
    label: "AI",
    items: [{ id: "ai", label: "AI providers", icon: Sparkles }],
  },
  {
    label: "Connections",
    items: [
      // The connections automations and workflow steps act through (Slack,
      // Gmail, Twilio, HTTP, GitHub's integration), to find, test or revoke.
      { id: "integrations", label: "Integrations", icon: Plug },
      { id: "git-connections", label: "GitHub", icon: Github },
      { id: "mcp", label: "Tools (MCP)", icon: McpIcon },
    ],
  },
  {
    label: "Machines & projects",
    items: [
      // Not gated on hasControlPlane: self-hosted machines, and the outside
      // AI apps granted access to each (its Access section), exist without
      // one — and Machines is the only place such a grant can be revoked.
      { id: "machines", label: "Machines", icon: Server },
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

/** True if the item should appear in the sidebar for the current build. */
function isItemVisible(item: SectionItem): boolean {
  if (item.id === "developer") return showDeveloperSettings();
  return true;
}

/** Section IDs in sidebar display order (About last, as the footer); used for keyboard nav. */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  return [...sectionGroups.flatMap((group) => group.items.filter(isItemVisible).map((section) => section.id)), ABOUT.id];
}

/** Whether `section` sits in a folded-away group (Advanced). */
export function isInCollapsibleGroup(section: SettingsSection): boolean {
  return sectionGroups.some((group) => group.collapsible && group.items.some((item) => item.id === section && isItemVisible(item)));
}

export function SettingsNavigation({
  activeSection,
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
    const isActive = section.id === activeSection;
    const item = (
      <button
        key={section.id}
        onClick={() => onSectionChange(section.id)}
        aria-label={section.label}
        aria-current={isActive ? "page" : undefined}
        data-section-id={section.id}
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
      <Tooltip key={section.id} content={section.label} side="right" triggerClassName="block w-full">
        {item}
      </Tooltip>
    ) : (
      item
    );
  };

  const build = appBuild();
  const version = formatVersion(build?.version, build?.commit);

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
          <button
            onClick={() => onSectionChange(ABOUT.id)}
            aria-label={ABOUT.label}
            aria-current={activeSection === ABOUT.id ? "page" : undefined}
            data-section-id={ABOUT.id}
            className={cn(
              "flex w-full cursor-pointer items-center gap-2.5 rounded-md border-l-2 px-3 py-1.5 text-left text-xs transition-all",
              activeSection === ABOUT.id
                ? "border-primary bg-primary/10 text-primary-ink font-medium"
                : "border-transparent text-muted-foreground hover:bg-foreground/5 hover:text-foreground"
            )}
          >
            <ABOUT.icon className="h-4 w-4 flex-shrink-0" />
            <span>{version ? `Reliant ${version} · About` : "About"}</span>
          </button>
        )}
      </div>
    </div>
  );
}
