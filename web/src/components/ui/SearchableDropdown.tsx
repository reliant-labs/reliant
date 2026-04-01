import { useState, useRef, useEffect } from "react";
import { ChevronDown, Search, Check, X } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "./Tooltip";

export interface DropdownOption {
  value: string;
  label: string;
  description?: string;
  icon?: React.ReactNode;
  metadata?: Record<string, unknown>;
  group?: string;
}

interface SearchableDropdownProps {
  options: DropdownOption[];
  value?: string;
  placeholder?: string;
  emptyMessage?: string;
  onSelect: (value: string | null) => void;
  className?: string;
  disabled?: boolean;
  clearable?: boolean;
  groupBy?: boolean;
  compact?: boolean;
  iconOnly?: boolean;
  dropdownDirection?: "up" | "down" | "auto";
  dropdownAlign?: "left" | "right";
  title?: string;
  variant?: "button" | "form";
}

export function SearchableDropdown({
  options,
  value,
  placeholder = "Search and select...",
  emptyMessage = "No options found",
  onSelect,
  className = "",
  disabled = false,
  clearable = false,
  groupBy = false,
  compact = false,
  iconOnly = false,
  dropdownAlign = "left",
  title = "",
  variant = "button",
}: SearchableDropdownProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const inputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Filter options based on search query
  const filteredOptions = options.filter(
    (option) =>
      option.label.toLowerCase().includes(searchQuery.toLowerCase()) ||
      option.description?.toLowerCase().includes(searchQuery.toLowerCase()) ||
      option.value.toLowerCase().includes(searchQuery.toLowerCase())
  );

  // Group options if requested
  const groupedOptions = groupBy
    ? filteredOptions.reduce((acc, option) => {
        const group = option.group || "Other";
        if (!acc[group]) acc[group] = [];
        acc[group].push(option);
        return acc;
      }, {} as Record<string, DropdownOption[]>)
    : { "": filteredOptions };

  const selectedOption = options.find((opt) => opt.value === value);

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlightedIndex((prev) =>
          prev < filteredOptions.length - 1 ? prev + 1 : prev
        );
        break;
      case "ArrowUp":
        e.preventDefault();
        setHighlightedIndex((prev) => (prev > 0 ? prev - 1 : prev));
        break;
      case "Enter":
        e.preventDefault();
        if (
          highlightedIndex >= 0 &&
          highlightedIndex < filteredOptions.length
        ) {
          handleSelect(filteredOptions[highlightedIndex]);
        }
        break;
      case "Escape":
        setIsOpen(false);
        break;
    }
  };

  const handleSelect = (option: DropdownOption) => {
    onSelect(option.value);
    setIsOpen(false);
    setSearchQuery("");
    setHighlightedIndex(-1);
  };

  const handleClear = (e: React.MouseEvent) => {
    e.stopPropagation();
    onSelect(null);
  };

  // Close dropdown when clicking outside - STANDARD PATTERN
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(event.target as Node)
      ) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  // Reset search and highlight when opening
  useEffect(() => {
    if (isOpen) {
      setSearchQuery("");
      setHighlightedIndex(-1);
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [isOpen]);

  const dropdownContent = (
      <div className={cn("relative", className)} ref={dropdownRef} data-dropdown-open={isOpen}>
        {/* Trigger Button */}
        <button
          onClick={() => !disabled && setIsOpen(!isOpen)}
          disabled={disabled}
          type="button"
          className={cn(
            "flex items-center text-sm transition-colors focus:outline-none",
            variant === "form"
              ? "w-full px-3 py-2 bg-[hsl(var(--config-input-bg))] border border-[hsl(var(--config-input-border))] rounded-md text-foreground focus:ring-2 focus:ring-ring/20 focus:border-ring"
              : "chat-button bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] font-medium rounded",
            variant === "button" && (iconOnly
              ? compact
                ? "px-1 h-6 justify-center w-6"
                : "px-1.5 h-7 justify-center"
              : compact
              ? "px-1.5 h-6 text-[10px] gap-1"
              : "px-2 h-7 gap-1 text-[10px]"),
            disabled && "opacity-50 cursor-not-allowed",
            !disabled && variant === "button" && "cursor-pointer hover:bg-[var(--chat-button-hover)]",
            !disabled && variant === "form" && "cursor-pointer hover:border-ring/40",
            className
          )}
        >
          {variant === "form" ? (
            // Form variant layout
            <>
              <div className="flex items-center gap-2.5 flex-1 min-w-0">
                {selectedOption?.icon && (
                  <div className="flex-shrink-0 text-muted-foreground">
                    {selectedOption.icon}
                  </div>
                )}
                <span className={cn("truncate text-sm font-medium", !value && "text-muted-foreground/70 font-normal")}>
                  {selectedOption?.label || placeholder}
                </span>
                {selectedOption?.description && (
                  <span className="text-xs px-1.5 py-0.5 rounded bg-muted/50 text-muted-foreground">
                    {selectedOption.description}
                  </span>
                )}
              </div>
              <ChevronDown
                className={cn(
                  "w-4 h-4 transition-transform flex-shrink-0 text-muted-foreground",
                  isOpen && "rotate-180"
                )}
              />
            </>
          ) : iconOnly ? (
            // Icon-only layout: just the icon
            <>
              {selectedOption?.icon}
            </>
          ) : compact ? (
            // Compact layout: icon + abbreviated text + chevron
            <>
              <div className="flex items-center gap-1 flex-1 min-w-0">
                {selectedOption?.icon}
                <span className="font-medium text-[10px] truncate">
                  {selectedOption?.label || placeholder}
                </span>
              </div>
              <div className="flex items-center flex-shrink-0">
                <ChevronDown
                  className={cn(
                    "w-3 h-3 transition-transform text-[var(--chat-button-text)]",
                    isOpen && "rotate-180"
                  )}
                />
              </div>
            </>
          ) : (
            // Full layout: icon, text, description, chevron
            <>
              <div className="flex items-center gap-2 flex-1 min-w-0">
                {selectedOption?.icon}
                <div className="flex-1 min-w-0">
                  <span className="font-medium">
                    {selectedOption?.label || placeholder}
                  </span>
                  {selectedOption?.description && (
                    <span className="text-xs text-[var(--chat-button-text)] ml-2">
                      {selectedOption.description}
                    </span>
                  )}
                </div>
              </div>
              <div className="flex items-center gap-1 flex-shrink-0">
                {clearable && value && (
                  <button
                    onClick={handleClear}
                    className="p-0.5 hover:bg-[var(--chat-button-bg)] rounded transition-colors cursor-pointer"
                    type="button"
                  >
                    <X className="w-3 h-3" />
                  </button>
                )}
                <ChevronDown
                  className={cn(
                    "w-4 h-4 transition-transform text-[var(--chat-button-text)]",
                    isOpen && "rotate-180"
                  )}
                />
              </div>
            </>
          )}
        </button>

        {/* Dropdown */}
        {isOpen && (
          <>
            <style>{`
              .searchable-dropdown {
                background-color: hsl(var(--popover)) !important;
              }
            `}</style>
            <div
              className={cn(
                `absolute searchable-dropdown flex flex-col ${
                  dropdownAlign === "right" ? "right-0" : "left-0"
                } z-[9999] rounded-lg border shadow-lg`,
                variant === "form"
                  ? "border-[hsl(var(--border))]/50 top-full mt-2 w-full"
                  : "border-border/50 " + (compact ? "bottom-full mb-1 w-64" : "top-full mt-1 w-full")
              )}
              style={{ 
                pointerEvents: 'auto',
                maxHeight: variant === "form" ? '400px' : '320px'
              }}
              onClick={(e) => {
                // Prevent dropdown clicks from closing parent or triggering handlers
                e.stopPropagation();
              }}
            >
            {/* Search Input */}
            <div className={cn(
              "p-2 border-b border-border/30 flex-shrink-0",
              variant === "form" ? "bg-muted/30" : "bg-muted/30"
            )}
            >
              <div className="relative">
                <Search className={cn(
                  "absolute left-2.5 top-1/2 transform -translate-y-1/2 w-3.5 h-3.5",
                  variant === "form" ? "text-[hsl(var(--muted-foreground))]" : "text-[var(--chat-button-text)]/60"
                )} />
                <input
                  ref={inputRef}
                  type="text"
                  value={searchQuery}
                  onChange={(e) => {
                    setSearchQuery(e.target.value);
                    setHighlightedIndex(-1);
                  }}
                  onKeyDown={handleKeyDown}
                  placeholder="Search..."
                  className={cn(
                    "w-full pl-9 pr-2.5 py-2 text-xs rounded-md transition-colors focus:outline-none focus:ring-1",
                    variant === "form"
                      ? "bg-[hsl(var(--config-input-bg))] border border-[hsl(var(--config-input-border))] text-foreground placeholder:text-muted-foreground focus:ring-ring/20 focus:border-ring"
                      : "bg-background border border-border text-foreground placeholder:text-muted-foreground hover:bg-accent/50 focus:ring-primary/50"
                  )}
                />
              </div>
            </div>

            {/* Options */}
            <div 
              className="overflow-y-auto overscroll-contain flex-1 min-h-0" 
            >
              {filteredOptions.length === 0 ? (
                <div className={cn(
                  "p-2.5 text-xs text-center",
                  variant === "form" ? "text-foreground/70" : "text-[var(--chat-button-text)]/60"
                )}>
                  {emptyMessage}
                </div>
              ) : (
                Object.entries(groupedOptions).map(
                  ([groupName, groupOptions]) => (
                    <div key={groupName}>
                      {groupBy && groupName && (
                        <div className={cn(
                          "px-2.5 py-1.5 text-[10px] font-bold uppercase tracking-wide border-b border-border/20 bg-muted/30 text-muted-foreground"
                        )}
                        >
                          {groupName}
                        </div>
                      )}
                      {groupOptions.map((option) => {
                        const globalIndex = filteredOptions.indexOf(option);
                        const isHighlighted = globalIndex === highlightedIndex;
                        const isSelected = option.value === value;

                        return (
                          <button
                            key={option.value}
                            onClick={() => handleSelect(option)}
                            onMouseEnter={() =>
                              setHighlightedIndex(globalIndex)
                            }
                            type="button"
                            className={cn(
                              "w-full text-left px-2.5 py-2 text-xs transition-colors text-foreground",
                              isSelected
                                ? "bg-accent font-semibold"
                                : isHighlighted
                                ? "bg-accent/50"
                                : "hover:bg-accent/50"
                            )}
                          >
                            <div className="flex items-start justify-between gap-2">
                              <div className="flex items-start gap-2 flex-1 min-w-0">
                                {option.icon && (
                                  <div className={cn(
                                    "flex-shrink-0 mt-0.5",
                                    variant === "form" && isSelected ? "text-primary" : "text-muted-foreground"
                                  )}>
                                    {option.icon}
                                  </div>
                                )}
                                <div className="flex-1 min-w-0">
                                  {/* Stack label and description vertically for better readability */}
                                  <div className={cn(
                                    "font-medium",
                                    variant === "form" && isSelected && "text-primary/80"
                                  )}>
                                    {option.label}
                                  </div>
                                  {option.description && (
                                    <div className={cn(
                                      "text-[10px] leading-snug mt-0.5 line-clamp-2",
                                      variant === "form"
                                        ? isSelected
                                          ? "text-primary/60"
                                          : "text-muted-foreground"
                                        : "text-[var(--chat-button-text)]/70"
                                    )}>
                                      {option.description}
                                    </div>
                                  )}
                                </div>
                              </div>
                              {isSelected && (
                                <Check className="w-4 h-4 text-primary flex-shrink-0 mt-0.5" />
                              )}
                            </div>
                          </button>
                        );
                      })}
                    </div>
                  )
                )
              )}
            </div>
          </div>
          </>
        )}
    </div>
  );

  return title ? (
    <Tooltip content={title} placement="top">
      {dropdownContent}
    </Tooltip>
  ) : (
    dropdownContent
  );
}
