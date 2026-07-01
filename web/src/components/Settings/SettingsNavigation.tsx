import { cn } from "../../lib/utils";
import { Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, FolderOpen, Globe, FolderGit2, Bell, KeyRound, Github, CreditCard, Server, Bot, ExternalLink } from "lucide-react";
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
  /**
   * Stable key. For in-app sections this is a routable {@link SettingsSection}.
   * For `external` items it's only used as a React key + collapsed-nav filter —
   * the click opens an external URL and never calls `onSectionChange`.
   */
  id: SettingsSection | string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  external?: boolean;
}

interface SectionGroup {
  label: string;
  items: SectionItem[];
}

const sectionGroups: SectionGroup[] = [
  {
    label: "Account",
    items: [
      { id: "account", label: "Account", icon: User },
    ],
  },
  {
    label: "Cloud",
    items: [
      // In-app cloud settings. These replaced the old external "Manage cloud
      // account" portal link — the overview / environments / AI / billing nav
      // now lives inside the app as first-party /settings sections backed by
      // controlplane.v1 public RPCs. Gated on hasControlPlane because they're
      // meaningless without a control-plane backend.
      ...(hasControlPlane
        ? [
            { id: "billing", label: "Billing", icon: CreditCard },
            { id: "environments", label: "Environments", icon: Server },
            { id: "reliant-ai", label: "Reliant AI", icon: Bot },
          ]
        : []),
      { id: "git-connections", label: "GitHub", icon: Github },
    ],
  },
  {
    label: "Workspace",
    items: [
      { id: "workspaces", label: "Workspaces", icon: FolderGit2 },
      { id: "projects", label: "Projects", icon: FolderOpen },
    ],
  },
  {
    label: "AI & Tools",
    items: [
      { id: "general", label: "AI", icon: Sparkles },
      { id: "mcp", label: "MCP Servers", icon: McpIcon },
      { id: "browser", label: "Web Browser", icon: Globe },
      { id: "prompts", label: "Prompts", icon: List },
    ],
  },
  {
    label: "Preferences",
    items: [
      { id: "appearance", label: "Appearance", icon: Monitor },
      { id: "shortcuts", label: "Keyboard Shortcuts", icon: Keyboard },
      { id: "notifications", label: "Notifications", icon: Bell },
      { id: "privacy", label: "Privacy", icon: Shield },
    ],
  },
  {
    label: "System",
    items: [
      { id: "tokens", label: "Access Tokens", icon: KeyRound },
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

/**
 * Routable section IDs in sidebar display order; used for keyboard nav.
 * External items (which open an external URL and never call `onSectionChange`)
 * are intentionally excluded so arrow-key nav never lands on a non-routable id.
 */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  return sectionGroups.flatMap((group) =>
    group.items
      .filter((section) => !section.external && isItemVisible(section))
      .map((section) => section.id as SettingsSection)
  );
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
                <div className="px-3 pb-1 pt-3 text-[10px] font-semibold uppercase tracking-[0.06em] text-muted-foreground/70">
                  {group.label}
                </div>
              )}
              <div className="space-y-0.5">
                {visibleItems.map((section) => {
                  const isActive = section.id === activeSection;
                  return (
                    <button
                      key={section.id}
                      onClick={() => onSectionChange(section.id as SettingsSection)}
                      title={isCollapsed ? section.label : undefined}
                      className={cn(
                        "w-full cursor-pointer rounded-md border-l-2 px-3 py-1.5 text-sm transition-all",
                        isCollapsed ? "flex items-center justify-center px-2" : "text-left",
                        isActive
                          ? "border-primary bg-primary/10 text-primary font-medium"
                          : "border-transparent text-muted-foreground hover:bg-muted/50 hover:text-foreground"
                      )}
                    >
                      <div className={cn("flex items-center", isCollapsed ? "justify-center" : "gap-2.5")}>
                        <section.icon className="h-4 w-4 flex-shrink-0" />
                        {!isCollapsed && (
                          <div className="flex flex-1 items-center justify-between">
                            <div className="font-medium">{section.label}</div>
                            {section.external && (
                              <ExternalLink className="h-3 w-3 flex-shrink-0 opacity-50" />
                            )}
                          </div>
                        )}
                      </div>
                    </button>
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