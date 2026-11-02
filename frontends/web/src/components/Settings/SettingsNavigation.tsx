import { cn } from "../../lib/utils";
import { Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, Lock, FolderOpen, Globe, GitBranch, Bell, KeyRound, Github, CreditCard, Server, Layers, Zap, MessageSquare, Plug } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { McpIcon } from "../icons/McpIcon";
import { hasControlPlane } from "../../services/controlPlane/config";
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
}

/**
 * Five groups, each a real category rather than a priority ("Essentials") or a
 * backend ("Cloud"):
 *   Account          who you are and what you pay for;
 *   AI               what your agents run on and the prompts and tools they get;
 *   Workflows        how runs are set up and when they need your approval;
 *   Machines & code  where agents work: machines, repositories and branches;
 *   App              how this app looks and behaves.
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
          ]
        : []),
    ],
  },
  {
    label: "AI",
    items: [
      { id: "ai", label: "AI providers", icon: Sparkles },
      { id: "mcp", label: "MCP servers", icon: McpIcon },
      { id: "prompts", label: "Prompts", icon: List },
    ],
  },
  {
    label: "Workflows",
    items: [
      { id: "presets", label: "Presets", icon: Layers },
      { id: "automations", label: "Automation approvals", icon: Zap },
    ],
  },
  {
    label: "Machines & code",
    items: [
      // Not gated on hasControlPlane: self-hosted machines, and the outside
      // AI apps granted access to each (its Access section), exist without
      // one — and Machines is the only place such a grant can be revoked.
      { id: "machines", label: "Machines", icon: Server },
      // Tokens exist to let a self-hosted machine or headless daemon connect,
      // so they sit with Machines.
      { id: "tokens", label: "Access tokens", icon: KeyRound },
      { id: "git-connections", label: "GitHub", icon: Github },
      // The connections automations and workflow steps act through (Slack,
      // Gmail, Twilio, HTTP, GitHub's integration), to find, test or revoke.
      { id: "integrations", label: "Integrations", icon: Plug },
      { id: "projects", label: "Projects", icon: FolderOpen },
      { id: "branches", label: "Branches", icon: GitBranch },
    ],
  },
  {
    label: "App",
    items: [
      { id: "appearance", label: "Appearance", icon: Monitor },
      { id: "chat-editor", label: "Chat & editor", icon: MessageSquare },
      // Reliant's own browser is app behaviour, not something agents run on.
      { id: "browser", label: "Built-in browser", icon: Globe },
      { id: "notifications", label: "Notifications", icon: Bell },
      { id: "shortcuts", label: "Keyboard shortcuts", icon: Keyboard },
      { id: "privacy", label: "Privacy", icon: Lock },
      { id: "about", label: "About", icon: Info },
      { id: "developer", label: "Developer", icon: Code },
    ],
  },
];

/**
 * Developer settings (Restart Backend, the mock driver, experimental flags)
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

/** Section IDs in sidebar display order; used for keyboard nav. */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  return sectionGroups.flatMap((group) => group.items.filter(isItemVisible).map((section) => section.id));
}

export function SettingsNavigation({
  activeSection,
  onSectionChange,
  isCollapsed = false,
}: SettingsNavigationProps) {
  return (
    <div className={cn("px-2 pb-4 pt-1", isCollapsed && "pb-2 pt-1")}>
      <nav className="space-y-1">
        {sectionGroups.map((group) => {
          const visibleItems = group.items.filter(isItemVisible);
          if (visibleItems.length === 0) return null;

          return (
            <div key={group.label}>
              {!isCollapsed && (
                <div className="px-3 pb-1 pt-3 text-xs font-medium text-muted-foreground">
                  {group.label}
                </div>
              )}
              <div className="space-y-0.5">
                {visibleItems.map((section) => {
                  const isActive = section.id === activeSection;
                  const item = (
                    <button
                      key={section.id}
                      onClick={() => onSectionChange(section.id)}
                      aria-label={section.label}
                      aria-current={isActive ? "page" : undefined}
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
                })}
              </div>
            </div>
          );
        })}
      </nav>
    </div>
  );
}
