import { useState } from "react";
import { Modal } from "../ui/Modal";
import { Input } from "../ui/Input";
import { Button } from "../ui/Button";
import { useUpdatePreferences } from "../../hooks/settings-queries";

interface FileOperationsModalProps {
  isOpen: boolean;
  onClose: () => void;
  operation: "newFile" | "newFolder" | "copy" | "delete" | null;
  currentPath: string;
  fileName?: string;
  onConfirm: (value: string) => void;
}

export function FileOperationsModal({
  isOpen,
  onClose,
  operation,
  currentPath,
  fileName,
  onConfirm,
}: FileOperationsModalProps) {
  const [inputValue, setInputValue] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [dontAskAgain, setDontAskAgain] = useState(false);
  const updatePreferences = useUpdatePreferences();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!inputValue.trim()) return;

    setIsSubmitting(true);
    try {
      await onConfirm(inputValue.trim());
      setInputValue("");
      onClose();
    } catch (error) {
      console.error("Operation failed:", error);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleClose = () => {
    setInputValue("");
    onClose();
  };

  const getTitle = () => {
    switch (operation) {
      case "newFile":
        return "Create New File";
      case "newFolder":
        return "Create New Folder";
      case "copy":
        return `Copy ${fileName}`;
      case "delete":
        return `Delete ${fileName}?`;
      default:
        return "";
    }
  };

  const getPlaceholder = () => {
    switch (operation) {
      case "newFile":
        return "Enter file name (e.g., index.tsx)";
      case "newFolder":
        return "Enter folder name";
      case "copy":
        return "Enter destination path";
      default:
        return "";
    }
  };

  const getLabel = () => {
    switch (operation) {
      case "newFile":
        return "File name";
      case "newFolder":
        return "Folder name";
      case "copy":
        return "Destination path";
      default:
        return "";
    }
  };

  const handleDeleteConfirm = async () => {
    // If "don't ask again" is checked, update the preference
    if (dontAskAgain) {
      try {
        await updatePreferences.mutateAsync({ skipDeleteConfirmation: true });
      } catch (error) {
        console.error("Failed to update preferences:", error);
      }
    }
    onConfirm("");
  };

  if (operation === "delete") {
    return (
      <Modal isOpen={isOpen} onClose={handleClose} title={getTitle()} size="sm">
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground font-mono">
            Are you sure you want to delete <strong>{fileName}</strong>?
            {currentPath && (
              <>
                <br />
                <span className="text-xs">Path: {currentPath}</span>
              </>
            )}
          </p>
          <p className="text-xs text-muted-foreground font-mono">
            You can restore this file using <strong>Cmd+Z</strong> (undo) or from source control.
          </p>
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="dont-ask-again"
              checked={dontAskAgain}
              onChange={(e) => setDontAskAgain(e.target.checked)}
              className="w-4 h-4 rounded border-border accent-primary cursor-pointer"
            />
            <label
              htmlFor="dont-ask-again"
              className="text-xs text-muted-foreground font-mono cursor-pointer select-none"
            >
              Do not ask me again
            </label>
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              onClick={handleClose}
              disabled={isSubmitting}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDeleteConfirm}
              disabled={isSubmitting}
            >
              {isSubmitting ? "Deleting..." : "Delete"}
            </Button>
          </div>
        </div>
      </Modal>
    );
  }

  return (
    <Modal isOpen={isOpen} onClose={handleClose} title={getTitle()} size="sm">
      <form onSubmit={handleSubmit} className="space-y-4">
        {currentPath && (
          <p className="text-xs text-muted-foreground font-mono">
            Location: {currentPath}
          </p>
        )}
        <div className="space-y-2">
          <label className="text-sm font-mono font-semibold">
            {getLabel()}
          </label>
          <Input
            type="text"
            value={inputValue}
            onChange={(e) => setInputValue(e.target.value)}
            placeholder={getPlaceholder()}
            autoFocus
            disabled={isSubmitting}
            className="font-mono"
          />
        </div>
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={handleClose}
            disabled={isSubmitting}
          >
            Cancel
          </Button>
          <Button type="submit" disabled={isSubmitting || !inputValue.trim()}>
            {isSubmitting ? "Creating..." : "Create"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}