import { MessageSquare, FolderOpen, FileText, History, Workflow, Bot, FolderGit2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { isDev } from "../../lib/constants";

export type NavigationTab =
  | "chats"
  | "project"
  | "worktrees"
  | "files"
  | "recent-changes"
  | "workflows"
  | "agents";

interface NavigationBarProps {
  activeTab: NavigationTab;
  onTabChange: (tab: NavigationTab) => void;
  isExpanded: boolean;
  onToggleExpanded: () => void;
}

interface NavItem {
  id: NavigationTab;
  icon: React.ElementType;
  label: string;
  tooltip: string;
  badge?: number;
}

const navItems: NavItem[] = [
  {
    id: "chats",
    icon: MessageSquare,
    label: "Chats",
    tooltip: "Chats",
  },
  {
    id: "files",
    icon: FileText,
    label: "Files",
    tooltip: "File Browser",
  },
  {
    id: "recent-changes",
    icon: History,
    label: "Changes",
    tooltip: "Recent Changes",
  },
  {
    id: "project",
    icon: FolderOpen,
    label: "Projects",
    tooltip: "Projects",
  },
  {
    id: "worktrees",
    icon: FolderGit2,
    label: "Workspaces",
    tooltip: "Workspaces",
  },
  // Workflows only visible in development mode
  ...(isDev ? [{
    id: "workflows" as const,
    icon: Workflow,
    label: "Workflows",
    tooltip: "Workflows (Dev Only)",
  }] : []),
  // Agents only visible in development mode
  ...(isDev ? [{
    id: "agents" as const,
    icon: Bot,
    label: "Agents",
    tooltip: "Agents (Dev Only)",
  }] : []),
];

export function NavigationBar({
  activeTab,
  onTabChange,
  isExpanded,
  onToggleExpanded,
}: NavigationBarProps) {
  return (
    <div
      className={cn(
        "elevation-1 flex flex-col transition-all duration-200 relative border-r border-border/40",
        isExpanded ? "w-12" : "w-12"
      )}
    >
      {/* (intentionally blank) - Brand is shown in the global Header */}

      {/* Navigation Items */}
      <div className="flex-1 flex flex-col">
        {navItems.map((item) => {
          const Icon = item.icon;
          const isActive = activeTab === item.id;

          return (
            <Tooltip
              key={item.id}
              content={item.tooltip}
              placement="right"
              delay={300}
            >
              <button
                onClick={() => {
                  if (activeTab === item.id) {
                    onToggleExpanded();
                  } else {
                    onTabChange(item.id);
                  }
                }}
                className={cn(
                  "w-full h-12 flex items-center justify-center relative transition-all duration-200",
                  "hover:bg-accent/80 hover:elevation-2",
                  isActive && "bg-accent/50"
                )}
                aria-label={item.label}
              >
                <Icon
                  className={cn(
                    "w-5 h-5 transition-colors duration-200",
                    isActive
                      ? ""
                      : "text-muted-foreground"
                  )}
                  style={isActive ? {
                    color: `hsl(var(--tab-active))`
                  } : undefined}
                />

                {/* Active indicator */}
                {isActive && (
                  <div
                    className="absolute left-0 top-2 bottom-2 w-0.5 rounded-r-full"
                    style={{ backgroundColor: `hsl(var(--tab-active-accent))` }}
                  />
                )}

                {/* Badge if present */}
                {item.badge && item.badge > 0 && (
                  <span className="absolute top-2 right-2 w-2 h-2 bg-primary rounded-full" />
                )}
              </button>
            </Tooltip>
          );
        })}
      </div>
    </div>
  );
}
