import { useState, useEffect, useRef, type ReactNode } from 'react';
import { cn } from '../../lib/utils';
import { useSidebarStore } from '../../store/sidebarStore';
import { usePanelSafety } from '../../hooks/usePanelSafety';

interface ResizableDiffPanelProps {
  children: ReactNode;
  defaultWidth?: number;
  minWidth?: number;
  maxWidth?: number;
  className?: string;
  hasTerminal?: boolean; // Whether terminal is open (affects height)
  isFullscreen?: boolean; // Whether panel is in fullscreen mode
  rightOffset?: number; // Offset from right edge (for sidebars) when in fullscreen mode
}

export function ResizableDiffPanel({
  children,
  defaultWidth: _defaultWidth = 600,
  minWidth = 400,
  maxWidth = 1200,
  className = '',
  hasTerminal = false,
  isFullscreen = false,
  rightOffset = 0
}: ResizableDiffPanelProps) {
  // Use shared sidebar store for width to sync with terminal
  const width = useSidebarStore((state) => state.width);
  const setWidth = useSidebarStore((state) => state.setWidth);
  const setIsResizingGlobal = useSidebarStore((state) => state.setIsResizing);
  const [isResizingWidth, setIsResizingWidth] = useState(false);
  const [isResizingHeight, setIsResizingHeight] = useState(false);
  // Get height percent from shared store
  const diffHeightPercent = useSidebarStore((state) => state.diffHeightPercent);
  const setDiffHeightPercent = useSidebarStore((state) => state.setDiffHeightPercent);
  const panelRef = useRef<HTMLDivElement>(null);
  const MIN_MAIN_CONTENT_WIDTH = 300;

  // Reactive safety check to ensure we don't crush the chat
  usePanelSafety({
    width,
    setWidth,
    minWidth,
    // We are the Right Panel (Viewer).
    // We need to account for Left Sidebar AND Right Sidebar (File Browser).
    subtractIds: ['layout-left-sidebar', 'layout-right-sidebar'],
    isEnabled: !isResizingWidth && !isFullscreen // Don't fight drag or fullscreen mode
  });

  useEffect(() => {
    // Validate width on mount (range check)
    // Safety hook handles the layout constraints check
    if (width < minWidth) {
      setWidth(minWidth);
    } else if (width > maxWidth) {
      setWidth(maxWidth);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Handle width resizing
  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isResizingWidth || !panelRef.current) return;

      // Calculate new width from the right edge
      const containerRight = panelRef.current.getBoundingClientRect().right;
      const newWidth = containerRight - e.clientX;

      // Safety check: Ensure we don't consume the entire screen
      // Leave at least MIN_MAIN_CONTENT_WIDTH for the main content
      
      // Calculate width of other panels (Left Sidebar and Right Sidebar/File Browser)
      let otherPanelsWidth = 0;
      const leftSidebar = document.getElementById('layout-left-sidebar');
      const rightSidebar = document.getElementById('layout-right-sidebar');
      
      if (leftSidebar) otherPanelsWidth += leftSidebar.getBoundingClientRect().width;
      if (rightSidebar) otherPanelsWidth += rightSidebar.getBoundingClientRect().width;

      const maxSafeWidth = window.innerWidth - otherPanelsWidth - MIN_MAIN_CONTENT_WIDTH;
      const effectiveMaxWidth = Math.min(maxWidth, maxSafeWidth);

      if (newWidth >= minWidth && newWidth <= effectiveMaxWidth) {
        setWidth(newWidth);
      }
    };

    const handleMouseUp = () => {
      setIsResizingWidth(false);
      setIsResizingGlobal(false);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };

    if (isResizingWidth) {
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'col-resize';
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };
  }, [isResizingWidth, width, minWidth, maxWidth, setWidth, setIsResizingGlobal]);

  // Handle height resizing (only when terminal is open)
  useEffect(() => {
    if (!hasTerminal) return;

    const handleMouseMove = (e: MouseEvent) => {
      if (!isResizingHeight || !panelRef.current) return;

      const containerRect = panelRef.current.parentElement?.getBoundingClientRect();
      if (!containerRect) return;

      const relativeY = e.clientY - containerRect.top;
      const newPercent = (relativeY / containerRect.height) * 100;
      
      // Clamp between 20% and 80%
      const clampedPercent = Math.max(20, Math.min(80, newPercent));
      setDiffHeightPercent(clampedPercent);
    };

    const handleMouseUp = () => {
      setIsResizingHeight(false);
      setIsResizingGlobal(false);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };

    if (isResizingHeight) {
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'row-resize';
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };
  }, [isResizingHeight, hasTerminal, setDiffHeightPercent, setIsResizingGlobal]);

  // Calculate height when terminal is open
  const calculatedHeight = hasTerminal 
    ? `${diffHeightPercent}%` 
    : '100%';

  return (
    <div
      ref={panelRef}
      className={cn(
        "relative bg-card flex flex-col border-l border-border transition-all duration-300 ease-in-out",
        isFullscreen && "absolute left-0 top-0 bottom-0 z-50 border-none",
        className
      )}
      style={isFullscreen ? {
        width: rightOffset > 0 ? `calc(100% - ${rightOffset}px)` : '100%',
        height: '100%',
        right: rightOffset > 0 ? `${rightOffset}px` : '0',
      } : { 
        width: `${width}px`,
        height: calculatedHeight,
        minHeight: hasTerminal ? '150px' : undefined, // Ensure minimum usable height when terminal is open
      }}
    >
      {/* Global Overlay for iframe protection during resize */}
      {(isResizingWidth || isResizingHeight) && (
        <div className="fixed inset-0 z-[9999] cursor-col-resize bg-transparent" style={{ cursor: isResizingHeight ? 'row-resize' : 'col-resize' }} />
      )}

      {/* Resize Handle - on the LEFT side for width */}
      {!isFullscreen && (
        <div
          className={cn(
            "absolute left-0 top-0 bottom-0 w-1 cursor-col-resize hover:bg-primary/50 transition-colors z-10 border-l border-border group",
            isResizingWidth && "bg-primary/80 w-1.5"
          )}
          onMouseDown={(e) => {
            e.preventDefault();
            setIsResizingWidth(true);
            setIsResizingGlobal(true);
          }}
        >
          <div className="absolute inset-y-0 -left-2 -right-2" />
        </div>
      )}

      {/* Resize Handle - on the BOTTOM side for height (only when terminal is open) */}
      {hasTerminal && !isFullscreen && (
        <div
          className={cn(
            "absolute bottom-0 left-0 right-0 h-1 cursor-row-resize hover:bg-primary/50 transition-colors z-20 group",
            isResizingHeight && "bg-primary/80 h-1.5"
          )}
          onMouseDown={(e) => {
            e.preventDefault();
            setIsResizingHeight(true);
            setIsResizingGlobal(true);
          }}
        >
          <div className="absolute inset-x-0 -top-2 -bottom-2" />
        </div>
      )}

      <div className="flex-1 flex flex-col overflow-hidden">
        {children}
      </div>
    </div>
  );
}
