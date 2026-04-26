import { cn } from "../../lib/utils";
import { Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, FolderOpen, Globe, FolderGit2, Bell, KeyRound } from "lucide-react";
import { McpIcon } from "../icons/McpIcon";

export type SettingsSection =
  | "account"
  | "general"
  | "shortcuts"
  | "prompts"
  | "workspaces"
  | "projects"
  | "browser"
  | "appearance"
  | "notifications"
  | "privacy"
  | "mcp"
  | "about"
  | "tokens"
  | "developer";

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

const sectionGroups: SectionGroup[] = [
  {
    label: "Account",
    items: [
      { id: "account", label: "Account", icon: User },
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

/** Section IDs in sidebar display order; use for keyboard nav so it matches the visible list. */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  const isElectron = window.RELIANT_CONFIG?.isElectron;
  const isDevelopment = isElectron ? window.RELIANT_CONFIG?.isDev : true;

  return sectionGroups.flatMap((group) =>
    group.items
      .filter((section) => {
        if (section.id === "developer") {
          return isDevelopment;
        }
        return true;
      })
      .map((section) => section.id)
  );
}

export function SettingsNavigation({
  activeSection,
  onSectionChange,
  isCollapsed = false,
}: SettingsNavigationProps) {
  const visibleIdSet = new Set(getVisibleSettingsSectionIds());

  return (
    <div className={cn("px-4 pb-4 pt-2", isCollapsed && "px-2 pb-2 pt-2")}>
      <nav className="space-y-0.5">
        {sectionGroups.map((group) => {
          const visibleItems = group.items.filter((item) => visibleIdSet.has(item.id));
          if (visibleItems.length === 0) return null;

          return (
            <div key={group.label}>
              {!isCollapsed && (
                <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60 px-3 pt-3 pb-1 font-medium">
                  {group.label}
                </div>
              )}
              <div className="space-y-0.5">
                {visibleItems.map((section) => {
                  const isActive = section.id === activeSection;
                  return (
                    <button
                      key={section.id}
                      onClick={() => onSectionChange(section.id)}
                      title={isCollapsed ? section.label : undefined}
                      className={cn(
                        "px-3 py-1.5 rounded-md cursor-pointer transition-all w-full",
                        isCollapsed ? "flex items-center justify-center" : "text-left",
                        isActive
                          ? "border-l-2 border-primary bg-primary/10 text-primary font-medium"
                          : "hover:bg-muted/50 text-foreground/80 hover:text-foreground border-l-2 border-transparent"
                      )}
                    >
                      <div className={cn("flex items-center", isCollapsed ? "" : "gap-2.5")}>
                        <section.icon className="w-4 h-4" />
                        {!isCollapsed && (
                          <div>
                            <div className="font-medium">{section.label}</div>
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
