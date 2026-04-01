/**
 * Ports Display Component
 * 
 * Elegant display of process ports with overflow dropdown.
 * Shows first N ports inline, with a dropdown for additional ports.
 */

import { useState, useEffect, useRef } from "react";
import { Globe, ChevronDown } from "lucide-react";
import { cn } from "../../lib/utils";

export interface PortsDisplayProps {
  ports: { port: number; protocol?: string }[];
  onOpenPort?: (port: number) => void;
  maxVisible?: number;
  /** Compact mode for list views */
  compact?: boolean;
}

export function PortsDisplay({ ports, onOpenPort, maxVisible = 2, compact = false }: PortsDisplayProps) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  
  // Sort ports by number for consistency
  const sortedPorts = [...ports].sort((a, b) => a.port - b.port);
  const visiblePorts = sortedPorts.slice(0, maxVisible);
  const overflowPorts = sortedPorts.slice(maxVisible);
  const hasOverflow = overflowPorts.length > 0;

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  const handlePortClick = (port: number, e?: React.MouseEvent) => {
    e?.stopPropagation();
    onOpenPort?.(port);
  };

  const PortButton = ({ port, showIcon = false }: { port: { port: number }; showIcon?: boolean }) => (
    <button
      onClick={(e) => handlePortClick(port.port, e)}
      disabled={!onOpenPort}
      className={cn(
        "flex items-center gap-0.5 font-mono text-primary",
        compact ? "text-xs" : "text-xs",
        onOpenPort && "hover:text-primary/80 hover:underline cursor-pointer"
      )}
      title={onOpenPort ? `Open localhost:${port.port} in browser` : `Port ${port.port}`}
    >
      {showIcon && <Globe className={cn(compact ? "w-3 h-3" : "w-3.5 h-3.5", "flex-shrink-0")} />}
      <span>{port.port}</span>
    </button>
  );

  return (
    <div className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
      {/* Visible ports */}
      {visiblePorts.map((p, idx) => (
        <PortButton key={p.port} port={p} showIcon={idx === 0} />
      ))}

      {/* Overflow dropdown */}
      {hasOverflow && (
        <div className="relative" ref={dropdownRef}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              setIsOpen(!isOpen);
            }}
            className={cn(
              "flex items-center gap-0.5 font-mono text-xs text-muted-foreground",
              "hover:text-foreground transition-colors"
            )}
            title={`${overflowPorts.length} more port${overflowPorts.length > 1 ? 's' : ''}`}
          >
            <span>+{overflowPorts.length}</span>
            <ChevronDown className={cn(
              "w-3 h-3 transition-transform",
              isOpen && "rotate-180"
            )} />
          </button>

          {isOpen && (
            <div className="absolute top-full mt-1 left-0 z-50 min-w-[100px] py-1 rounded-md border bg-popover shadow-md">
              {overflowPorts.map((p) => (
                <button
                  key={p.port}
                  onClick={(e) => {
                    handlePortClick(p.port, e);
                    setIsOpen(false);
                  }}
                  disabled={!onOpenPort}
                  className={cn(
                    "w-full px-3 py-1.5 text-left font-mono text-xs",
                    "hover:bg-accent transition-colors",
                    onOpenPort ? "text-primary" : "text-muted-foreground"
                  )}
                >
                  :{p.port}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
