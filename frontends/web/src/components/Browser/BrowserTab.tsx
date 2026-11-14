import { X, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";

interface BrowserTabProps {
  id: string;
  title: string;
  favicon?: string;
  isActive: boolean;
  isLoading: boolean;
  onSelect: () => void;
  onClose: (e: React.MouseEvent) => void;
}

export function BrowserTab({
  id: _id,
  title,
  favicon,
  isActive,
  isLoading,
  onSelect,
  onClose,
}: BrowserTabProps) {
  return (
    <div
      className={cn(
        "group flex items-center gap-2 px-3 py-1.5 min-w-[120px] max-w-[200px] border-r border-border cursor-pointer transition-colors",
        isActive
          ? "bg-background text-foreground"
          : "bg-muted/50 text-muted-foreground hover:bg-muted"
      )}
      onClick={onSelect}
      role="tab"
      aria-selected={isActive}
      aria-label={title}
    >
      {/* Favicon or loading indicator */}
      <div className="flex-shrink-0 w-4 h-4 flex items-center justify-center">
        {isLoading ? (
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
        ) : favicon ? (
          <img
            src={favicon}
            alt=""
            className="w-4 h-4 object-contain"
            onError={(e) => {
              // Fallback to globe icon on error
              e.currentTarget.style.display = "none";
            }}
          />
        ) : (
          <div className="w-3 h-3 rounded-full bg-muted-foreground/20" />
        )}
      </div>

      {/* Title */}
      <span className="flex-1 text-xs truncate">{title}</span>

      {/* Close button */}
      <button
        onClick={onClose}
        className={cn(
          "flex-shrink-0 p-0.5 rounded hover:bg-accent transition-colors",
          !isActive && "opacity-0 group-hover:opacity-100"
        )}
        aria-label={`Close ${title}`}
      >
        <X className="w-3 h-3" />
      </button>
    </div>
  );
}
