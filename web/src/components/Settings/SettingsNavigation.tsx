import { cn } from "../../lib/utils";
import { Sparkles, Keyboard, Info, List, Monitor, Code, User, Shield, FolderOpen, Globe, FolderGit2, Bell, MessageSquare, KeyRound } from "lucide-react";
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
  | "feedback"
  | "about"
  | "tokens"
  | "developer";

interface SettingsNavigationProps {
  activeSection: SettingsSection;
  onSectionChange: (section: SettingsSection) => void;
  isCollapsed?: boolean;
}

const settingsSections = [
  {
    id: "account" as const,
    label: "Account",
    icon: User,
  },
  {
    id: "general" as const,
    label: "AI",
    icon: Sparkles,
  },
  {
    id: "workspaces" as const,
    label: "Workspaces",
    icon: FolderGit2,
  },
  {
    id: "projects" as const,
    label: "Projects",
    icon: FolderOpen,
  },
  {
    id: "appearance" as const,
    label: "Appearance",
    icon: Monitor,
  },
  {
    id: "mcp" as const,
    label: "MCP Servers",
    icon: McpIcon,
  },
  {
    id: "shortcuts" as const,
    label: "Keyboard Shortcuts",
    icon: Keyboard,
  },
  {
    id: "prompts" as const,
    label: "Prompts",
    icon: List,
  },
  {
    id: "browser" as const,
    label: "Web Browser",
    icon: Globe,
  },
  {
    id: "notifications" as const,
    label: "Notifications",
    icon: Bell,
  },
  {
    id: "privacy" as const,
    label: "Privacy",
    icon: Shield,
  },
  {
    id: "tokens" as const,
    label: "Access Tokens",
    icon: KeyRound,
  },
  {
    id: "feedback" as const,
    label: "Send Feedback",
    icon: MessageSquare,
  },
  {
    id: "about" as const,
    label: "About",
    icon: Info,
  },
  {
    id: "developer" as const,
    label: "Developer",
    icon: Code,
  },
] as const;

/** Section IDs in sidebar display order; use for keyboard nav so it matches the visible list. */
export function getVisibleSettingsSectionIds(): SettingsSection[] {
  const isElectron = window.RELIANT_CONFIG?.isElectron;
  const isDevelopment = isElectron ? window.RELIANT_CONFIG?.isDev : true;

  return settingsSections
    .filter((section) => {
      if (section.id === "developer") {
        return isDevelopment;
      }
      return true;
    })
    .map((section) => section.id);
}

export function SettingsNavigation({
  activeSection,
  onSectionChange,
  isCollapsed = false,
}: SettingsNavigationProps) {
  const visibleIdSet = new Set(getVisibleSettingsSectionIds());
  const visibleSections = settingsSections.filter((section) => visibleIdSet.has(section.id));

  return (
    <div className={cn("px-4 pb-4 pt-2", isCollapsed && "px-2 pb-2 pt-2")}>
      <nav className="space-y-2">
        {visibleSections.map((section) => (
          <button
            key={section.id}
            onClick={() => onSectionChange(section.id)}
            title={isCollapsed ? section.label : undefined}
            className={cn(
              "p-3 rounded-lg border-2 cursor-pointer transition-all w-full",
              isCollapsed ? "flex items-center justify-center" : "text-left",
              section.id === activeSection
                ? "font-semibold"
                : "border-transparent text-foreground hover:bg-accent/50 hover:border-border"
            )}
            style={
              section.id === activeSection
                ? {
                    backgroundColor: "hsl(var(--primary) / 0.1)",
                    color: "hsl(var(--primary))",
                    borderColor: "hsl(var(--primary))",
                  }
                : undefined
            }
          >
            <div className={cn("flex items-center", isCollapsed ? "" : "gap-3")}>
              <section.icon className="w-5 h-5" />
              {!isCollapsed && (
                <div>
                  <div className="font-medium">{section.label}</div>
                </div>
              )}
            </div>
          </button>
        ))}
      </nav>
    </div>
  );
}