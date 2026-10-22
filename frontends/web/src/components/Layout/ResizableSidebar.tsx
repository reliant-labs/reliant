import { useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '../../lib/utils';
import { useSidebarStore } from '../../store/sidebarStore';
import { usePanelSafety } from '../../hooks/usePanelSafety';

interface ResizableSidebarProps {
  children: ReactNode;
  defaultWidth?: number;
  minWidth?: number;
  maxWidth?: number;
  className?: string;
  side?: 'left' | 'right';
  storageKey?: string;
}

export function ResizableSidebar({
  children,
  defaultWidth = 240,
  minWidth = 240,
  maxWidth = 800,
  className = '',
  side = 'left',
  storageKey
}: ResizableSidebarProps) {
  const key = storageKey || `${side}-sidebar-width`;
  
  const [width, setWidth] = useState(() => {
    // Load saved width from localStorage
    const saved = localStorage.getItem(key);
    return saved ? parseInt(saved, 10) : defaultWidth;
  });
  const [isResizing, setIsResizing] = useState(false);
  const setIsResizingGlobal = useSidebarStore((state) => state.setIsResizing);
  const sidebarRef = useRef<HTMLDivElement>(null);

  const MIN_MAIN_CONTENT_WIDTH = 300;

  // Reactive safety check to ensure we don't crush the chat
  usePanelSafety({
    width,
    setWidth,
    minWidth,
    // If we are left, subtract right panel & right sidebar. 
    // If we are right, subtract left sidebar.
    subtractIds: side === 'left'
      ? ['layout-right-panel', 'layout-right-sidebar']
      : ['layout-left-sidebar', 'layout-right-panel'],
    isEnabled: !isResizing // Don't fight the user while dragging, let the drag handler manage limits
  });

  useEffect(() => {
    // Validate persisted width on mount (basic range check)
    // The safety hook handles the complex "safe zone" check on mount too
    if (width < minWidth) {
      setWidth(minWidth);
    } else if (width > maxWidth) {
      setWidth(maxWidth);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // Run once on mount

  useEffect(() => {
    const handleMouseMove = (e: MouseEvent) => {
      if (!isResizing || !sidebarRef.current) return;
      
      // Use getBoundingClientRect for accurate positioning
      const rect = sidebarRef.current.getBoundingClientRect();
      
      let newWidth;
      if (side === 'right') {
        // For right sidebar, calculate width from the right edge
        newWidth = rect.right - e.clientX;
      } else {
        // For left sidebar, calculate from left edge
        newWidth = e.clientX - rect.left;
      }
      
      // Safety check: Ensure we don't consume the entire screen
      // Leave at least MIN_MAIN_CONTENT_WIDTH for the main content
      
      // Calculate width of other panels that might be consuming space
      let otherPanelsWidth = 0;
      
      if (side === 'left') {
        // If we are the left sidebar, check for right panels
        const rightPanel = document.getElementById('layout-right-panel');
        const rightSidebar = document.getElementById('layout-right-sidebar');
        if (rightPanel) otherPanelsWidth += rightPanel.getBoundingClientRect().width;
        if (rightSidebar) otherPanelsWidth += rightSidebar.getBoundingClientRect().width;
      } else {
        // If we are the right sidebar, check for left sidebar and right panel
        const leftSidebar = document.getElementById('layout-left-sidebar');
        const rightPanel = document.getElementById('layout-right-panel');
        
        if (leftSidebar) otherPanelsWidth += leftSidebar.getBoundingClientRect().width;
        if (rightPanel) otherPanelsWidth += rightPanel.getBoundingClientRect().width;
      }

      const maxSafeWidth = window.innerWidth - otherPanelsWidth - MIN_MAIN_CONTENT_WIDTH;
      const effectiveMaxWidth = Math.min(maxWidth, maxSafeWidth);

      if (newWidth >= minWidth && newWidth <= effectiveMaxWidth) {
        setWidth(newWidth);
      }
    };

    const handleMouseUp = () => {
      setIsResizing(false);
      setIsResizingGlobal(false);
      // Save width to localStorage when resizing ends
      localStorage.setItem(key, width.toString());
    };

    if (isResizing) {
      document.addEventListener('mousemove', handleMouseMove);
      document.addEventListener('mouseup', handleMouseUp);
      // Prevent text selection while resizing
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'col-resize';
    }

    return () => {
      document.removeEventListener('mousemove', handleMouseMove);
      document.removeEventListener('mouseup', handleMouseUp);
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    };
  }, [isResizing, width, minWidth, maxWidth, key, side, setIsResizingGlobal, setWidth]);

  const borderClass = side === 'right' ? 'border-l' : 'border-r';
  const handlePosition = side === 'right' ? 'left-0' : 'right-0';

  return (
    <div
      ref={sidebarRef}
      className={cn("relative bg-background border-border flex h-full flex-shrink-0", borderClass, className)}
      style={{ width: `${width}px`, flexShrink: 0, backgroundColor: 'hsl(var(--background))' }}
    >
      <div className="flex-1 flex flex-col overflow-hidden bg-background" style={{ backgroundColor: 'hsl(var(--background))' }}>
        {children}
      </div>
      
      {/* Resize Overlay - Global transparent overlay to capture events over iframes */}
      {isResizing && (
        <div className="fixed inset-0 z-[9999] cursor-col-resize bg-transparent" />
      )}

      {/* Resize Handle */}
      <div
        className={cn(
          // Keep below floating setup guide/checklist (z-40) and modal layers.
          "absolute top-0 bottom-0 w-1 cursor-col-resize hover:bg-primary/50 transition-colors group z-30",
          handlePosition,
          isResizing && "bg-primary/80 w-1.5"
        )}
        onMouseDown={(e) => {
          e.preventDefault();
          setIsResizing(true);
          setIsResizingGlobal(true);
        }}
      >
        {/* Wider invisible hit area for easier grabbing */}
        <div className={cn("absolute inset-y-0 -left-2 -right-2")} />
      </div>
    </div>
  );
}