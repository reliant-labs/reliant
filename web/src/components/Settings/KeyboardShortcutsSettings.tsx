import { useState, useEffect, useRef } from "react";
import { cn } from "../../lib/utils";
import { useShortcutsStore } from "../../store/shortcutsStore";
import { Button } from "../ui/Button";
import { RotateCcw, Edit3, Check, X, AlertTriangle, Keyboard, Globe } from "lucide-react";
import { toast } from "../../lib/toast-manager";
import {
  chordToString,
  isSequence,
  parseBinding,
  sequencePrefix,
} from "../../lib/keyboard/chord";
import { detectPlatform, formatBinding } from "../../lib/keyboard/platform";

/** Render an authored binding ("Cmd+K C") for display. */
function formatAuthored(authored: string): string {
  const { isMac } = detectPlatform();
  if (!authored) return "";
  return formatBinding(parseBinding(authored, isMac), isMac);
}

/**
 * Whether an authored chord already opens a sequence somewhere in the app.
 *
 * Derived from the live shortcut set rather than a hardcoded "Cmd+K", so if the
 * prefix ever changes — or a user rebinds onto a different one — capture keeps
 * working without this needing to be updated.
 */
function isSequencePrefix(authoredChord: string, isMac: boolean): boolean {
  const canonical = parseBinding(authoredChord, isMac);
  const { shortcuts } = useShortcutsStore.getState();
  const { isDesktop } = detectPlatform();

  return Object.values(shortcuts).some((shortcut) => {
    const authored =
      shortcut.currentBinding ??
      (isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding);
    if (!authored) return false;

    const binding = parseBinding(authored, isMac);
    return isSequence(binding) && sequencePrefix(binding) === canonical;
  });
}

/**
 * Convert a captured event into AUTHORED form ("Cmd+Shift+P").
 *
 * We store what the user meant, not the platform-resolved chord, so a binding
 * set on a Mac still reads as Ctrl on Windows.
 */
function eventToAuthored(e: KeyboardEvent, isMac: boolean): string {
  const canonical = chordToString({
    key: e.key,
    ctrl: e.ctrlKey,
    meta: e.metaKey,
    shift: e.shiftKey,
    alt: e.altKey,
  });

  const parts = canonical.split("+");
  const key = parts.pop() ?? "";
  const out: string[] = [];

  const hasCtrl = parts.includes("ctrl");
  const hasMeta = parts.includes("meta");

  // Fold the platform-primary modifier back to the portable "Cmd" spelling.
  if (isMac ? hasMeta : hasCtrl) out.push("Cmd");
  // A second, literal Control (Mac) or Alt-as-second (PC) becomes "Ctrl".
  if (isMac && hasCtrl && hasMeta) out.push("Ctrl");
  else if (!isMac && hasCtrl && parts.includes("alt")) out.push("Ctrl");
  else if (!isMac && hasMeta) out.push("Win");

  if (parts.includes("shift")) out.push("Shift");
  if (parts.includes("alt") && !(!isMac && hasCtrl)) out.push("Alt");

  out.push(key);
  return out.join("+");
}

// Key input component for capturing key combinations
function KeyInput({
  value,
  onChange,
  onCancel,
  onSave,
  conflictWarning,
  reservationWarning,
}: {
  value: string;
  onChange: (binding: string) => void;
  onCancel: () => void;
  onSave: () => void;
  conflictWarning?: string;
  reservationWarning?: string;
}) {
  const inputRef = useRef<HTMLDivElement>(null);
  const [isCapturing, setIsCapturing] = useState(false);
  // Armed first chord of a two-key capture. Mirrored in a ref because the
  // keydown listener is registered once and must read the live value.
  const [pendingPrefix, setPendingPrefix] = useState<string | null>(null);
  const pendingPrefixRef = useRef<string | null>(null);

  useEffect(() => {
    if (inputRef.current) {
      inputRef.current.focus();
      setIsCapturing(true);
    }
  }, []);

  useEffect(() => {
    if (!isCapturing) return;

    const { isMac } = detectPlatform();

    const handleKeyDown = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopPropagation();

      // Modifier-only presses are not a chord.
      if (["Control", "Meta", "Alt", "Shift"].includes(e.key)) return;

      // Escape cancels rather than being captured — otherwise there is no way
      // out of the capture box with the keyboard. Mid-sequence it backs out of
      // the half-typed binding first, so one Escape does not discard the whole
      // edit just because the user mistyped the second key.
      if (e.key === "Escape" && !e.ctrlKey && !e.metaKey && !e.altKey) {
        if (pendingPrefixRef.current) {
          pendingPrefixRef.current = null;
          setPendingPrefix(null);
          onChange("");
          return;
        }
        onCancel();
        return;
      }

      const chord = eventToAuthored(e, isMac);

      // Sequences have to be capturable, or a user could never rebind onto one
      // — and 24 of the shipped defaults are sequences, so "Cmd+K then T" would
      // be a shape the app uses but the settings UI cannot express.
      //
      // A chord that is a known sequence PREFIX starts a two-key capture: hold
      // it and wait for the completing key. Anything else replaces the binding
      // outright, so a plain remap still takes one keystroke.
      if (pendingPrefixRef.current) {
        onChange(`${pendingPrefixRef.current} ${chord}`);
        pendingPrefixRef.current = null;
        setPendingPrefix(null);
        return;
      }

      if (isSequencePrefix(chord, isMac)) {
        pendingPrefixRef.current = chord;
        setPendingPrefix(chord);
        onChange(chord);
        return;
      }

      onChange(chord);
    };

    document.addEventListener("keydown", handleKeyDown, true);
    return () => document.removeEventListener("keydown", handleKeyDown, true);
  }, [isCapturing, onChange, onCancel]);

  return (
    <div className="space-y-2">
      {isCapturing && (
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <Keyboard className="w-3 h-3" />
          {pendingPrefix
            ? "Now press the second key, or Esc to cancel"
            : "Listening for key combination... (Esc to cancel)"}
        </div>
      )}

      <div className="flex flex-col sm:flex-row items-stretch sm:items-center gap-2">
        <div
          ref={inputRef}
          tabIndex={0}
          className={cn(
            "flex-1 px-3 py-2 border border-border/40 rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring min-h-[38px] flex items-center justify-center",
            conflictWarning && "border-destructive bg-destructive/5",
            // Mid-sequence: the value shown is not yet a complete binding.
            pendingPrefix && "border-primary/60 bg-primary/5"
          )}
        >
          <span className="font-mono text-sm text-center">
            {value ? formatAuthored(value) : "Press key combination..."}
            {pendingPrefix && (
              <span className="text-muted-foreground"> then…</span>
            )}
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
            // Saving mid-sequence would store a bare prefix, which shadows
            // every sequence that starts with it.
            disabled={!value || !!conflictWarning || !!pendingPrefix}
          >
            <Check className="w-4 h-4" />
          </Button>
          <Button size="sm" variant="ghost" onClick={onCancel}>
            <X className="w-4 h-4" />
          </Button>
        </div>
      </div>

      {/* Advisory, not blocking: the user may know better than our table. */}
      {reservationWarning && !conflictWarning && (
        <div className="flex items-start gap-1 text-xs text-amber-600 dark:text-amber-500">
          <Globe className="w-3 h-3 mt-0.5 shrink-0" />
          <span>{reservationWarning}</span>
        </div>
      )}
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
    getEffectiveBinding,
    findConflict,
    getReservationFor,
  } = useShortcutsStore();

  const shortcut = shortcuts[shortcutId];
  const [tempBinding, setTempBinding] = useState<string>("");

  if (!shortcut) return null;

  const effective = getEffectiveBinding(shortcutId);
  const isEditingThis = isEditing === shortcutId;
  const isDefault = shortcut.currentBinding === null;

  const conflict =
    isEditingThis && tempBinding
      ? findConflict(tempBinding, shortcutId)
      : undefined;
  const conflictWarning = conflict
    ? `Already used by "${conflict.name}"`
    : undefined;

  const reservation =
    isEditingThis && tempBinding ? getReservationFor(tempBinding) : undefined;
  const reservationWarning = reservation
    ? reservation.level === "hard"
      ? `Your browser ${reservation.reason} with this combination and will not pass it to Reliant — this shortcut would never fire. Try a Cmd+K sequence instead.`
      : `Your browser ${reservation.reason} with this combination. Reliant will override it.`
    : undefined;

  const handleEdit = () => {
    setTempBinding(effective);
    setEditing(shortcutId);
  };

  const handleCancel = () => {
    setTempBinding("");
    setEditing(null);
  };

  const handleSave = async () => {
    if (tempBinding && !conflictWarning) {
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

                reservationWarning={reservationWarning}
              />
            ) : (
              <kbd className="px-2 py-1 bg-muted rounded border border-border/40 text-xs font-mono min-w-[120px] text-center">
                {formatAuthored(effective)}
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

                  reservationWarning={reservationWarning}
                />
              </div>
            ) : (
              <>
                <kbd className="px-2 py-1 bg-muted rounded border border-border/40 text-xs font-mono flex-1 text-center">
                  {formatAuthored(effective)}
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
          formatAuthored(
            shortcut.currentBinding ?? shortcut.defaultBinding,
          )
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
    (shortcut) => shortcut.currentBinding !== null
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