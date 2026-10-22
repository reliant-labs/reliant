import { ChevronLeft, ChevronRight, RotateCw } from "lucide-react";
import { useBrowserStore } from "../../store/browserStore";
import { Tooltip } from "../ui/Tooltip";
import { cn } from "../../lib/utils";

export function NavigationControls() {
  const activeTab = useBrowserStore((state) => state.getActiveTab());
  const goBack = useBrowserStore((state) => state.goBack);
  const goForward = useBrowserStore((state) => state.goForward);
  const reload = useBrowserStore((state) => state.reload);

  if (!activeTab) return null;

  const handleGoBack = () => {
    if (activeTab.canGoBack) {
      goBack(activeTab.id);
    }
  };

  const handleGoForward = () => {
    if (activeTab.canGoForward) {
      goForward(activeTab.id);
    }
  };

  const handleReload = () => {
    reload(activeTab.id);
  };

  return (
    <div className="flex items-center gap-1 px-2">
      {/* Back button */}
      <Tooltip content="Back" placement="bottom" delay={300}>
        <button
          onClick={handleGoBack}
          disabled={!activeTab.canGoBack}
          className={cn(
            "p-1.5 rounded transition-colors",
            activeTab.canGoBack
              ? "hover:bg-accent text-foreground"
              : "text-muted-foreground/40 cursor-not-allowed"
          )}
          aria-label="Go Back"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
      </Tooltip>

      {/* Forward button */}
      <Tooltip content="Forward" placement="bottom" delay={300}>
        <button
          onClick={handleGoForward}
          disabled={!activeTab.canGoForward}
          className={cn(
            "p-1.5 rounded transition-colors",
            activeTab.canGoForward
              ? "hover:bg-accent text-foreground"
              : "text-muted-foreground/40 cursor-not-allowed"
          )}
          aria-label="Go Forward"
        >
          <ChevronRight className="w-4 h-4" />
        </button>
      </Tooltip>

      {/* Reload button */}
      <Tooltip content="Reload" placement="bottom" delay={300}>
        <button
          onClick={handleReload}
          className={cn(
            "p-1.5 rounded hover:bg-accent transition-colors",
            activeTab.isLoading && "animate-spin"
          )}
          aria-label="Reload"
        >
          <RotateCw className="w-4 h-4" />
        </button>
      </Tooltip>
    </div>
  );
}
