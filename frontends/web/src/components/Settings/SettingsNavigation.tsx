import { cn } from "../../lib/utils";
import { Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, Lock, FolderOpen, Globe, GitBranch, Bell, KeyRound, Github, CreditCard, Server, Layers, Zap } from "lucide-react";
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
 * Four groups, each a real category rather than a priority ("Essentials") or a
 * backend ("Cloud"):
 *   Account  who you are and what you pay for;
 *   AI       what your agents run on and how they are set up;
 *   Work     the projects, branches and machines they work in;
 *   App      how this app looks and behaves.
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
      { id: "presets", label: "Presets", icon: Layers },
      { id: "browser", label: "Web browser", icon: Globe },
    ],
  },
  {
    label: "Work",
    items: [
      { id: "projects", label: "Projects", icon: FolderOpen },
      { id: "branches", label: "Branches", icon: GitBranch },
      { id: "automations", label: "Automations", icon: Zap },
      // Not gated on hasControlPlane: self-hosted machines, and the outside
      // AI apps granted access to each (its Access section), exist without
      // one — and Machines is the only place such a grant can be revoked.
      { id: "machines", label: "Machines", icon: Server },
      { id: "git-connections", label: "GitHub", icon: Github },
      // Tokens exist to let a self-hosted machine or headless daemon connect,
      // so they sit with Machines.
      { id: "tokens", label: "Access tokens", icon: KeyRound },
    ],
  },
  {
    label: "App",
    items: [
      { id: "appearance", label: "Appearance", icon: Monitor },
      { id: "shortcuts", label: "Keyboard shortcuts", icon: Keyboard },
      { id: "notifications", label: "Notifications", icon: Bell },
      { id: "privacy", label: "Privacy", icon: Lock },
      { id: "about", label: "About", icon: Info },
      { id: "developer", label: "Developer", icon: Code },
    ],
  },
];

function isDevBuild(): boolean {
  const isElectron = window.RELIANT_CONFIG?.isElectron;
  return isElectron ? Boolean(window.RELIANT_CONFIG?.isDev) : true;
}

/** True if the item should appear in the sidebar for the current build. */
function isItemVisible(item: SectionItem): boolean {
  if (item.id === "developer") return isDevBuild();
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
                          ? "border-primary bg-primary/10 text-primary font-medium"
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
