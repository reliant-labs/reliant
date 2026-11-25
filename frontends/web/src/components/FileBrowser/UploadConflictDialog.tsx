import Modal from "../forge-ui/modal";
import Button from "../forge-ui/button";
import { focusChatInput } from "../../hooks/useFocusManager";
import type { ConflictChoice } from "../../api/fileUpload";
import { useFileUploadStore } from "../../store/fileUploadStore";

/** How a destination folder reads in upload UI; "" is the workspace root. */
export function uploadFolderLabel(directory: string): string {
  return directory === "" || directory === "/" ? "the project root" : directory;
}

interface UploadConflictDialogProps {
  /** The clash being asked about; null keeps the dialog closed. */
  conflict: { name: string; directory: string } | null;
  onChoose: (choice: ConflictChoice) => void;
}

/**
 * "“report.xlsx” already exists in docs": Replace, Keep both, or Cancel.
 *
 * Cancel takes focus when it opens, so a stray Enter never overwrites a
 * file, and Escape (or the close button, or the scrim) is Cancel too.
 */
export function UploadConflictDialog({ conflict, onChoose }: UploadConflictDialogProps) {
  return (
    <Modal
      open={conflict !== null}
      onClose={() => onChoose("cancel")}
      fallbackFocus={focusChatInput}
      title={conflict ? `“${conflict.name}” already exists in ${uploadFolderLabel(conflict.directory)}` : undefined}
      size="sm"
      footer={
        <>
          <Button variant="ghost" autoFocus onClick={() => onChoose("cancel")}>
            Cancel
          </Button>
          <Button variant="outline" onClick={() => onChoose("keep-both")}>
            Keep both
          </Button>
          <Button variant="danger" onClick={() => onChoose("replace")}>
            Replace
          </Button>
        </>
      }
    >
      <p className="text-ui text-muted-foreground">
        Replace overwrites it. Keep both saves the upload under a new name, like “
        {conflict ? keepBothExample(conflict.name) : ""}”.
      </p>
    </Modal>
  );
}

/** "report.xlsx" → "report (1).xlsx": the first name Keep both tries. */
function keepBothExample(name: string): string {
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return `${name} (1)`;
  return `${name.slice(0, dot)} (1)${name.slice(dot)}`;
}

/** The dialog for whichever upload the queue is waiting on. */
export function FileUploadConflictPrompt() {
  const conflict = useFileUploadStore((state) => state.conflict);
  const resolveConflict = useFileUploadStore((state) => state.resolveConflict);
  return <UploadConflictDialog conflict={conflict} onChoose={resolveConflict} />;
}
