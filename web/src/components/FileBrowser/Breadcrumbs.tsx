import { ChevronRight, Home } from "lucide-react";

interface BreadcrumbsProps {
  path: string;
  onPathChange: (path: string) => void;
}

export function Breadcrumbs({ path, onPathChange }: BreadcrumbsProps) {
  const segments = path.split("/").filter(Boolean);

  const handleSegmentClick = (index: number) => {
    if (index === -1) {
      onPathChange("/");
    } else {
      const newPath = "/" + segments.slice(0, index + 1).join("/");
      onPathChange(newPath);
    }
  };

  return (
    <div className="flex items-center gap-1 text-sm overflow-x-auto">
      <button
        onClick={() => handleSegmentClick(-1)}
        className="flex items-center gap-1 px-2 py-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
        title="Root"
      >
        <Home className="w-3.5 h-3.5" />
      </button>

      {segments.map((segment, index) => (
        <div key={index} className="flex items-center gap-1">
          <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          <button
            onClick={() => handleSegmentClick(index)}
            className={`px-2 py-1 rounded hover:bg-muted transition-colors ${
              index === segments.length - 1
                ? "text-foreground font-semibold"
                : "text-muted-foreground hover:text-foreground"
            }`}
          >
            {segment}
          </button>
        </div>
      ))}
    </div>
  );
}
