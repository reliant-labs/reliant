# Dropdown Component Standards

## The Standard Pattern

ALL dropdowns in the codebase MUST use this exact pattern for consistent behavior:

### 1. Required Imports
```tsx
import { useState, useRef, useEffect } from "react";
```

### 2. State and Refs
```tsx
const [isOpen, setIsOpen] = useState(false);
const containerRef = useRef<HTMLDivElement>(null);
```

### 3. Click-Outside Handler (CRITICAL)
```tsx
// Close dropdown when clicking outside - STANDARD PATTERN
useEffect(() => {
  const handleClickOutside = (event: MouseEvent) => {
    if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
      setIsOpen(false);
    }
  };

  if (isOpen) {
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }
}, [isOpen]);
```

### 4. Container with Ref
```tsx
<div className="relative" ref={containerRef}>
  {/* Trigger button */}
  <button onClick={() => setIsOpen(!isOpen)}>
    Open Dropdown
  </button>

  {/* Dropdown content */}
  {isOpen && (
    <div className="absolute ...">
      {/* dropdown items */}
    </div>
  )}
</div>
```

## ❌ NEVER Use Backdrop Pattern

**DO NOT** use this pattern:
```tsx
{/* ❌ BAD - Don't use backdrop divs */}
{isOpen && (
  <div className="fixed inset-0 z-40" onClick={() => setIsOpen(false)} />
)}
```

**Why?** Backdrops cause z-index stacking issues and don't work reliably.

## Consistent Styling

### Chat Context Dropdowns
```tsx
className="bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] border-[var(--chat-border)] hover:bg-[var(--chat-button-hover)]"
```

### Form Dropdowns
```tsx
className="bg-background text-foreground border-border hover:border-primary/50"
```

### Dropdown Menu Positioning
```tsx
// Opens upward (for bottom UI elements like chat input)
className="absolute bottom-full mb-1 left-0 z-50"

// Opens downward (for top UI elements)
className="absolute top-full mt-1 left-0 z-50"
```

## Components Following Standard

✅ **Correct Implementation:**
- `AgentTaskforceSelector.tsx`
- `PromptsSelector.tsx`
- `SearchableDropdown.tsx`
- `UnifiedPillSelector.tsx` (worktree section)
- `WorktreeSelector.tsx`

## Tooltip Integration

Tooltips automatically hide when dropdowns open because:
1. Click events hide tooltips
2. `isInteractionActive` state prevents re-show
3. Modal detection hides tooltips

No special handling needed in dropdown components.

## Reusable Component

Use `web/src/components/ui/Dropdown.tsx` for new dropdowns with consistent behavior out of the box.

## Testing Checklist

When implementing a dropdown:
- [ ] Uses `mousedown` listener pattern
- [ ] Ref on container element
- [ ] No backdrop div
- [ ] Consistent chat/form styling
- [ ] Tooltips hide on click
- [ ] Closes when clicking outside
- [ ] Closes when clicking dropdown items
- [ ] Works with keyboard (Escape key)
