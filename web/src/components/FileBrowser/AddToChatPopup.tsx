/**
 * AddToChatPopup - Popup that appears when text is selected in Monaco editor
 * Enhanced visibility with better contrast and styling
 * Uses React Portal to render at document root for true fixed positioning
 */

import { Plus } from 'lucide-react';
import { createPortal } from 'react-dom';
import { cn } from '../../lib/utils';

interface AddToChatPopupProps {
  visible: boolean;
  x: number;
  y: number;
  onAdd: () => void;
  shortcut?: string;
  container?: HTMLElement | null;
}

export function AddToChatPopup({
  visible,
  x,
  y,
  onAdd,
  shortcut = window.navigator.platform.includes("Mac") ? '⌘L' : 'Ctrl+L',
  container,
}: AddToChatPopupProps) {
  if (!visible) return null;

  const popupContent = (
    <>
      <style>{`
        .add-to-chat-popup-button {
          background-color: hsl(var(--primary)) !important;
          color: hsl(var(--primary-foreground)) !important;
          opacity: 1 !important;
          border: 1px solid hsl(var(--primary-foreground) / 0.25) !important;
          box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25) !important;
          backdrop-filter: none !important;
        }
        .add-to-chat-popup-button:hover {
          background-color: hsl(var(--primary) / 0.92) !important;
          color: hsl(var(--primary-foreground)) !important;
          opacity: 1 !important;
        }
        .add-to-chat-popup-button .add-to-chat-shortcut {
          background-color: hsl(var(--primary-foreground) / 0.22) !important;
          color: hsl(var(--primary-foreground)) !important;
        }
        @keyframes fadeInScale {
          from {
            opacity: 0;
            transform: scale(0.9);
          }
          to {
            opacity: 1;
            transform: scale(1);
          }
        }
      `}</style>
      <div
        style={{
          position: 'fixed', // Always fixed to viewport - portal target doesn't affect this
          left: `${x}px`, // Absolute viewport X coordinate
          top: `${y}px`, // Absolute viewport Y coordinate
          zIndex: 1, // Low z-index to appear underneath Monaco's fold overlay (like the highlighted line)
          pointerEvents: 'none', // Allow clicks to pass through container
          transform: 'none',
        }}
      >
        <button
          onClick={(e) => {
            e.stopPropagation();
            e.preventDefault();
            onAdd();
          }}
          className={cn(
            'add-to-chat-popup-button',
            'pointer-events-auto',
            'flex items-center gap-1.5 px-2.5 py-1 rounded-md',
            'text-xs font-medium',
            'active:scale-95',
            'transition-all duration-150'
          )}
          style={{
            animation: 'fadeInScale 0.15s ease-out',
            zIndex: 1,
          }}
        >
        <Plus className="w-3 h-3 shrink-0" strokeWidth={2.5} />
        <span className="font-medium">Add to Chat</span>
        <span className="add-to-chat-shortcut text-[10px] font-mono px-1.5 py-0.5 rounded">
          {shortcut}
        </span>
      </button>
    </div>
    </>
  );

  // Render to container (file browser) or document body using portal
  // If container is provided, render there to keep it within file browser bounds
  // Otherwise render to body for true fixed positioning
  const portalTarget = container || document.body;
  return createPortal(popupContent, portalTarget);
}
