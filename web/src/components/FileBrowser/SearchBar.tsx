import { Search, X, Loader2 } from "lucide-react";

interface SearchBarProps {
  value: string;
  onChange: (value: string) => void;
  isSearching?: boolean;
}

export function SearchBar({ value, onChange, isSearching = false }: SearchBarProps) {
  return (
    <div className="relative w-full">
      {isSearching ? (
        <Loader2 className="absolute left-2 top-1/2 transform -translate-y-1/2 w-3.5 h-3.5 text-primary animate-spin" />
      ) : (
        <Search className="absolute left-2 top-1/2 transform -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground" />
      )}
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="Search files..."
        className="w-full pl-8 pr-8 py-1.5 text-xs font-mono bg-background border border-border/60 rounded focus:outline-none focus:ring-2 focus:ring-primary/50 transition-all"
      />
      {value && (
        <button
          onClick={() => onChange("")}
          className="absolute right-2 top-1/2 transform -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
          aria-label="Clear search"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      )}
    </div>
  );
}
