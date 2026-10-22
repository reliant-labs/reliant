import { useState, useEffect, useRef } from "react";
import { Lock, Search } from "lucide-react";
import { useBrowserStore } from "../../store/browserStore";
import { cn } from "../../lib/utils";

export function AddressBar() {
  const activeTab = useBrowserStore((state) => state.getActiveTab());
  const navigateTab = useBrowserStore((state) => state.navigateTab);
  const [inputValue, setInputValue] = useState("");
  const [isFocused, setIsFocused] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  // Update input value when active tab changes
  useEffect(() => {
    if (activeTab && !isFocused) {
      setInputValue(activeTab.url);
    }
  }, [activeTab, isFocused]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!activeTab || !inputValue.trim()) return;

    let url = inputValue.trim();

    // Add protocol if missing
    if (!url.match(/^https?:\/\//i)) {
      // Check if it looks like a domain (contains a dot and no spaces)
      if (url.includes(".") && !url.includes(" ")) {
        url = `https://${url}`;
      } else {
        // Treat as search query
        url = `https://www.google.com/search?q=${encodeURIComponent(url)}`;
      }
    }

    navigateTab(activeTab.id, url);
    inputRef.current?.blur();
  };

  const handleFocus = () => {
    setIsFocused(true);
    // Select all text on focus for easy editing
    inputRef.current?.select();
  };

  const handleBlur = () => {
    setIsFocused(false);
    // Reset to actual URL on blur
    if (activeTab) {
      setInputValue(activeTab.url);
    }
  };

  if (!activeTab) {
    return (
      <div className="flex-1 flex items-center px-3 py-1.5 bg-muted/30 rounded text-muted-foreground text-sm">
        No active tab
      </div>
    );
  }

  const isSecure = activeTab.url.startsWith("https://");

  return (
    <form onSubmit={handleSubmit} className="flex-1 flex items-center gap-2 px-3">
      <div
        className={cn(
          "flex-1 flex items-center gap-2 px-3 py-1.5 bg-background border rounded transition-colors",
          isFocused ? "border-ring" : "border-border"
        )}
      >
        {/* Security/Search icon */}
        <div className="flex-shrink-0">
          {isFocused ? (
            <Search className="w-4 h-4 text-muted-foreground" />
          ) : isSecure ? (
            <Lock className="w-3.5 h-3.5 text-green-600" />
          ) : (
            <Search className="w-4 h-4 text-muted-foreground" />
          )}
        </div>

        {/* URL input */}
        <input
          ref={inputRef}
          type="text"
          value={inputValue}
          onChange={(e) => setInputValue(e.target.value)}
          onFocus={handleFocus}
          onBlur={handleBlur}
          placeholder="Search or enter address"
          className="flex-1 bg-transparent text-sm outline-none"
          aria-label="Address bar"
        />
      </div>
    </form>
  );
}
