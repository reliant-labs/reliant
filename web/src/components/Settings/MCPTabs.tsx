import { cn } from "../../lib/utils";

export interface MCPTabItem {
  id: string;
  label: string;
  shortLabel?: string;
  count?: number;
}

interface MCPTabsProps {
  tabs: MCPTabItem[];
  activeTab: string;
  onTabChange: (tabId: string) => void;
  className?: string;
}

export function MCPTabs({ tabs, activeTab, onTabChange, className }: MCPTabsProps) {
  const activeIndex = tabs.findIndex((tab) => tab.id === activeTab);

  const focusTabByIndex = (index: number) => {
    const normalizedIndex = (index + tabs.length) % tabs.length;
    const tabId = tabs[normalizedIndex]?.id;
    if (tabId) {
      onTabChange(tabId);
    }
  };

  const handleTabKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, tabIndex: number) => {
    if (tabs.length === 0) return;

    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown": {
        event.preventDefault();
        focusTabByIndex(tabIndex + 1);
        break;
      }
      case "ArrowLeft":
      case "ArrowUp": {
        event.preventDefault();
        focusTabByIndex(tabIndex - 1);
        break;
      }
      case "Home": {
        event.preventDefault();
        focusTabByIndex(0);
        break;
      }
      case "End": {
        event.preventDefault();
        focusTabByIndex(tabs.length - 1);
        break;
      }
      default:
        break;
    }
  };

  return (
    <div
      role="tablist"
      aria-label="MCP settings sections"
      className={cn(
        "inline-flex w-full items-center gap-1 overflow-x-auto rounded-xl border border-border/60 bg-muted/40 p-1",
        className
      )}
    >
      {tabs.map((tab, index) => {
        const isActive = tab.id === activeTab;

        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={isActive}
            aria-controls={`mcp-tabpanel-${tab.id}`}
            id={`mcp-tab-${tab.id}`}
            tabIndex={isActive || (activeIndex === -1 && index === 0) ? 0 : -1}
            onClick={() => onTabChange(tab.id)}
            onKeyDown={(event) => handleTabKeyDown(event, index)}
            className={cn(
              "inline-flex min-w-max shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium transition-colors md:min-w-0 md:flex-1",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              isActive
                ? "border border-border/60 bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:bg-background/60 hover:text-foreground"
            )}
          >
            <span className="sm:hidden">{tab.shortLabel ?? tab.label}</span>
            <span className="hidden sm:inline">{tab.label}</span>
            {typeof tab.count === "number" && (
              <span
                className={cn(
                  "rounded-full px-1.5 py-0.5 text-xs",
                  isActive
                    ? "bg-primary/10 text-primary"
                    : "bg-muted text-muted-foreground"
                )}
              >
                {tab.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}