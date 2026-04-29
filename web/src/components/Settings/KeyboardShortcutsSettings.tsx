import { useState, useEffect, useRef, useMemo } from "react";
import { cn } from "../../lib/utils";
import { useShortcutsStore, type KeyBinding } from "../../store/shortcutsStore";
import { Button } from "../ui/Button";
import { RotateCcw, Edit3, Check, X, AlertTriangle, Keyboard } from "lucide-react";
import { toast } from "../../lib/toast-manager";

// Helper function to format key combinations for display
function formatKeyBinding(binding: KeyBinding): string {
  const isMac =
    typeof window !== "undefined" &&
    (window.navigator.platform.toUpperCase().includes("MAC") ||
      window.navigator.userAgent.toUpperCase().includes("MAC"));

  const parts: string[] = [];

  if (binding.ctrl && !isMac) parts.push("Ctrl");
  if (binding.meta && isMac) parts.push("Cmd");
  if (binding.meta && !isMac) parts.push("Win");
  if (binding.ctrl && isMac) parts.push("Ctrl");
  if (binding.alt) parts.push(isMac ? "Option" : "Alt");
  if (binding.shift) parts.push("Shift");

  // Format special keys
  let key = binding.key;
  if (key === "Escape") key = "Esc";
  else if (key === "ArrowLeft") key = "←";
  else if (key === "ArrowRight") key = "→";
  else if (key === "ArrowUp") key = "↑";
  else if (key === "ArrowDown") key = "↓";
  else if (key === " ") key = "Space";
  else if (key === "PageUp") key = "PgUp";
  else if (key === "PageDown") key = "PgDn";
  else if (key === "Enter") key = "↵";
  else if (key === "Tab") key = "⇥";
  else if (key === "Backspace") key = "⌫";
  else if (key === "Delete") key = "⌦";

  parts.push(key);
  return parts.join(" + ");
}

// Key input component for capturing key combinations
function KeyInput({
  value,
  onChange,
  onCancel,
  onSave,
  conflictWarning,
}: {
  value: KeyBinding;
  onChange: (binding: KeyBinding) => void;
  onCancel: () => void;
  onSave: () => void;
  conflictWarning?: string;
}) {
  const inputRef = useRef<HTMLDivElement>(null);
  const [isCapturing, setIsCapturing] = useState(false);

  useEffect(() => {
    if (inputRef.current) {
      inputRef.current.focus();
      setIsCapturing(true);
    }
  }, []);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (!isCapturing) return;

      e.preventDefault();
      e.stopPropagation();

      // Don't capture modifier-only presses
      if (["Control", "Meta", "Alt", "Shift"].includes(e.key)) return;

      // Normalize letter keys to uppercase for consistent matching
      let normalizedKey = e.key;
      if (normalizedKey.length === 1 && /[a-zA-Z]/.test(normalizedKey)) {
        normalizedKey = normalizedKey.toUpperCase();
      }

      const binding: KeyBinding = {
        key: normalizedKey,
        ctrl: e.ctrlKey,
        meta: e.metaKey,
        shift: e.shiftKey,
        alt: e.altKey,
      };

      onChange(binding);
    };

    if (isCapturing) {
      document.addEventListener("keydown", handleKeyDown, true);
      return () => document.removeEventListener("keydown", handleKeyDown, true);
    }
  }, [isCapturing, onChange]);

  return (
    <div className="space-y-2">
      {isCapturing && (
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <Keyboard className="w-3 h-3" />
          Listening for key combination...
        </div>
      )}

      <div className="flex flex-col sm:flex-row items-stretch sm:items-center gap-2">
        <div
          ref={inputRef}
          tabIndex={0}
          className={cn(
            "flex-1 px-3 py-2 border border-border/40 rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring min-h-[38px] flex items-center justify-center",
            conflictWarning && "border-destructive bg-destructive/5"
          )}
        >
          <span className="font-mono text-sm text-center">
            {value.key ? formatKeyBinding(value) : "Press key combination..."}
          </span>
        </div>

        {conflictWarning && (
          <div className="flex items-center gap-1 text-xs text-destructive sm:hidden">
            <AlertTriangle className="w-3 h-3" />
            <span>Conflict</span>
          </div>
        )}

        <div className="flex items-center gap-2 justify-end">
          {conflictWarning && (
            <div className="hidden sm:flex items-center gap-1 text-xs text-destructive">
              <AlertTriangle className="w-3 h-3" />
              <span>Conflict</span>
            </div>
          )}

          <Button
            size="sm"
            variant="primary"
            onClick={onSave}
            disabled={!value.key || !!conflictWarning}
          >
            <Check className="w-4 h-4" />
          </Button>
          <Button size="sm" variant="ghost" onClick={onCancel}>
            <X className="w-4 h-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}

// Individual shortcut row component
function ShortcutRow({ shortcutId }: { shortcutId: string }) {
  const {
    shortcuts,
    isEditing,
    setEditing,
    updateShortcut,
    resetShortcut,
    isKeyComboTaken,
  } = useShortcutsStore();

  const shortcut = shortcuts[shortcutId];
  const [tempBinding, setTempBinding] = useState<KeyBinding>(
    shortcut?.currentBinding || { key: '', ctrl: false, meta: false, shift: false, alt: false }
  );

  // Normalize tempBinding key for conflict checking
  const normalizedTempBinding = useMemo(() => {
    if (!tempBinding.key) return tempBinding;
    let normalizedKey = tempBinding.key;
    if (normalizedKey.length === 1 && /[a-zA-Z]/.test(normalizedKey)) {
      normalizedKey = normalizedKey.toUpperCase();
    }
    return { ...tempBinding, key: normalizedKey };
  }, [tempBinding]);

  // Early return after all hooks
  if (!shortcut) return null;

  const isEditingThis = isEditing === shortcutId;
  const isDefault =
    JSON.stringify(shortcut.currentBinding) ===
    JSON.stringify(shortcut.defaultBinding);

  const conflictWarning =
    isEditingThis && tempBinding.key
      ? isKeyComboTaken(normalizedTempBinding, shortcutId)
        ? "This key combination is already in use"
        : undefined
      : undefined;

  const handleEdit = () => {
    setTempBinding(shortcut.currentBinding);
    setEditing(shortcutId);
  };

  const handleCancel = () => {
    setTempBinding(shortcut.currentBinding);
    setEditing(null);
  };

  const handleSave = async () => {
    if (tempBinding.key && !conflictWarning) {
      try {
        await updateShortcut(shortcutId, tempBinding);
        setEditing(null);
        toast.success(`Updated shortcut for "${shortcut.name}"`);
      } catch {
        toast.error(`Failed to update shortcut for "${shortcut.name}"`);
      }
    }
  };

  const handleReset = async () => {
    try {
      await resetShortcut(shortcutId);
      setEditing(null);
      toast.success(`Reset shortcut for "${shortcut.name}" to default`);
    } catch {
      toast.error(`Failed to reset shortcut for "${shortcut.name}"`);
    }
  };

  return (
    <div
      className={cn(
        "group px-4 py-3 hover:elevation-1 transition-colors",
        isEditingThis && "elevation-1"
      )}
    >
      <div className="space-y-3">
        {/* Desktop layout - single row */}
        <div className="hidden sm:flex items-center justify-between gap-4">
          <div className="flex-1 min-w-0">
            <div className="font-medium text-sm">{shortcut.name}</div>
            <div className="text-xs text-muted-foreground">
              {shortcut.description}
            </div>
          </div>

          <div className="flex items-center gap-3">
            {isEditingThis ? (
              <KeyInput
                value={tempBinding}
                onChange={setTempBinding}
                onCancel={handleCancel}
                onSave={handleSave}
                conflictWarning={conflictWarning}
              />
            ) : (
              <kbd className="px-2 py-1 bg-muted rounded border border-border/40 text-xs font-mono min-w-[120px] text-center">
                {formatKeyBinding(shortcut.currentBinding)}
              </kbd>
            )}

            <div className="flex items-center gap-1">
              {!isEditingThis && (
                <>
                  <Button size="sm" variant="outline" onClick={handleEdit}>
                    <Edit3 className="w-3 h-3" />
                  </Button>
                  {!isDefault && (
                    <Button size="sm" variant="ghost" onClick={handleReset}>
                      <RotateCcw className="w-3 h-3" />
                    </Button>
                  )}
                </>
              )}
            </div>
          </div>
        </div>

        {/* Mobile layout - stacked */}
        <div className="sm:hidden space-y-3">
          <div>
            <div className="font-medium text-sm">{shortcut.name}</div>
            <div className="text-xs text-muted-foreground">
              {shortcut.description}
            </div>
          </div>

          <div className="flex items-center justify-between gap-3">
            {isEditingThis ? (
              <div className="flex-1">
                <KeyInput
                  value={tempBinding}
                  onChange={setTempBinding}
                  onCancel={handleCancel}
                  onSave={handleSave}
                  conflictWarning={conflictWarning}
                />
              </div>
            ) : (
              <>
                <kbd className="px-2 py-1 bg-muted rounded border border-border/40 text-xs font-mono flex-1 text-center">
                  {formatKeyBinding(shortcut.currentBinding)}
                </kbd>
                <div className="flex items-center gap-1">
                  <Button size="sm" variant="outline" onClick={handleEdit}>
                    <Edit3 className="w-3 h-3" />
                  </Button>
                  {!isDefault && (
                    <Button size="sm" variant="ghost" onClick={handleReset}>
                      <RotateCcw className="w-3 h-3" />
                    </Button>
                  )}
                </div>
              </>
            )}
          </div>
        </div>

        {conflictWarning && (
          <div className="text-xs text-destructive flex items-center gap-1">
            <AlertTriangle className="w-3 h-3" />
            {conflictWarning}
          </div>
        )}
      </div>
    </div>
  );
}

export function KeyboardShortcutsSettings() {
  const { shortcuts, resetAllShortcuts, initializeShortcuts, isLoading } =
    useShortcutsStore();
  const [searchQuery, setSearchQuery] = useState("");

  useEffect(() => {
    initializeShortcuts();
  }, [initializeShortcuts]);

  // Group shortcuts by category
  const categories = Object.values(shortcuts).reduce((acc, shortcut) => {
    if (!acc[shortcut.category]) {
      acc[shortcut.category] = [];
    }
    acc[shortcut.category].push(shortcut);
    return acc;
  }, {} as Record<string, (typeof shortcuts)[keyof typeof shortcuts][]>);

  // Filter shortcuts based on search query
  const filteredCategories = Object.entries(categories).reduce(
    (acc, [category, shortcuts]) => {
      const filtered = shortcuts.filter(
        (shortcut) =>
          shortcut.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
          shortcut.description
            .toLowerCase()
            .includes(searchQuery.toLowerCase()) ||
          formatKeyBinding(shortcut.currentBinding)
            .toLowerCase()
            .includes(searchQuery.toLowerCase())
      );
      if (filtered.length > 0) {
        acc[category] = filtered;
      }
      return acc;
    },
    {} as Record<string, (typeof shortcuts)[keyof typeof shortcuts][]>
  );

  const hasCustomizations = Object.values(shortcuts).some(
    (shortcut) =>
      JSON.stringify(shortcut.currentBinding) !==
      JSON.stringify(shortcut.defaultBinding)
  );

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-base font-semibold mb-2">Keyboard Shortcuts</h2>
        <p className="text-sm text-muted-foreground">
          Customize keyboard shortcuts to match your workflow. Click the edit
          button to record a new key combination.
        </p>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-8">
          <div className="text-muted-foreground">Loading shortcuts...</div>
        </div>
      ) : (
        <>

      {/* Search and Reset Controls */}
      <div className="flex items-center justify-between gap-4">
        <div className="relative flex-1 max-w-sm">
          <input
            type="text"
            placeholder="Search shortcuts..."
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full px-3 py-2 border border-border/40 rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring text-sm"
          />
        </div>

        {hasCustomizations && (
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              if (
                window.confirm(
                  "Reset all shortcuts to defaults? This cannot be undone."
                )
              ) {
                try {
                  await resetAllShortcuts();
                  toast.success("All shortcuts reset to defaults");
                } catch {
                  toast.error("Failed to reset shortcuts");
                }
              }
            }}
            className="flex items-center gap-2"
            rightIcon={<RotateCcw className="w-4 h-4" />}
          >
            Reset All
          </Button>
        )}
      </div>

      {/* Shortcuts List */}
      <div className="space-y-6">
        {Object.entries(filteredCategories).map(([category, shortcuts]) => (
          <div key={category}>
            <h3 className="text-sm font-semibold text-foreground mb-3">
              {category}
            </h3>
            <div className="border border-border/40 rounded-lg overflow-hidden bg-card">
              {shortcuts.map((shortcut, index) => (
                <div key={shortcut.id}>
                  <ShortcutRow shortcutId={shortcut.id} />
                  {index < shortcuts.length - 1 && (
                    <div className="border-t border-border/40" />
                  )}
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>

      {Object.keys(filteredCategories).length === 0 && searchQuery && (
        <div className="text-center py-8 text-muted-foreground">
          <Keyboard className="w-8 h-8 mx-auto mb-2 opacity-50" />
          <p>No shortcuts found matching "{searchQuery}"</p>
        </div>
      )}

      {/* Tips */}
      <div className="mt-8 p-4 elevation-1 rounded-lg border border-border/40">
        <h4 className="text-sm font-semibold mb-2 flex items-center gap-2">
          <Keyboard className="w-4 h-4" />
          Tips
        </h4>
        <ul className="text-sm text-muted-foreground space-y-1">
          <li>
            • Click the edit button and press your desired key combination
          </li>
          <li>
            • Most shortcuts support Ctrl/Cmd + modifiers for best compatibility
          </li>
          <li>• Changes take effect immediately - no restart required</li>
          <li>
            • Use the reset button to restore individual shortcuts to defaults
          </li>
          <li>• Avoid system shortcuts like Cmd+Q, Cmd+H, or Alt+Tab</li>
        </ul>
      </div>
        </>
      )}
    </div>
  );
}