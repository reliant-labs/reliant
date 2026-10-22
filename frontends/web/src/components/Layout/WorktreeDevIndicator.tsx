import { cn } from "../../lib/utils";
import { useEffect, useState } from "react";
import { isDev } from "../../lib/constants";

// Vite injects this at build/dev time from vite.config.ts
declare const __WORKTREE_NAME__: string;

interface WorktreeDevIndicatorProps {
  worktreeName?: string;
  className?: string;
}

/**
 * WorktreeDevIndicator - Dev-only indicator showing current worktree name
 * 
 * Displays a high-contrast pink/purple badge with the current worktree name
 * to help developers track which worktree instance they're working in.
 * 
 * Only visible in development mode (isDev === true)
 */
export function WorktreeDevIndicator({ 
  worktreeName: propWorktreeName, 
  className 
}: WorktreeDevIndicatorProps) {
  const [displayName, setDisplayName] = useState<string>('dev');
  
  useEffect(() => {
    // Priority 1: Use prop if provided (explicit override)
    if (propWorktreeName?.trim()) {
      setDisplayName(propWorktreeName.trim());
      return;
    }
    
    // Priority 2: Use Vite-injected worktree name (from directory at build/dev time)
    // This is the correct value - it represents the actual directory where the app was started
    // and remains constant throughout the session, unlike the store which changes when
    // switching/creating worktrees within the app
    try {
      if (typeof __WORKTREE_NAME__ !== 'undefined' && __WORKTREE_NAME__) {
        setDisplayName(__WORKTREE_NAME__);
        return;
      }
    } catch (e) {
      // __WORKTREE_NAME__ not defined
    }
    
    // Priority 3: Try window global (fallback for browser)
    const globalWorktreeName = (window as any).__WORKTREE_NAME__;
    if (globalWorktreeName?.trim()) {
      setDisplayName(globalWorktreeName.trim());
      return;
    }
    
    // Fallback: just use 'dev'
    setDisplayName('dev');
  }, [propWorktreeName]);

  // Only render in dev mode
  if (!isDev) {
    return null;
  }

  return (
    <div
      className={cn(
        "px-2.5 py-1 rounded-md text-xs font-semibold cursor-default",
        "bg-purple-600/30 text-purple-100 border-2 border-purple-400/50",
        "flex items-center select-none",
        "backdrop-blur-sm shadow-lg",
        className
      )}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      title={`Development Worktree: ${displayName}`}
    >
      <span className="text-purple-50">{displayName}</span>
    </div>
  );
}
