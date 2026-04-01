import { Modal } from "../ui/Modal";
import { Textarea } from "../ui/Textarea";
import { Calendar, Lock } from "lucide-react";

interface WorkflowInfoPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  description: string;
  onDescriptionChange?: (desc: string) => void;
  createdAt?: string;
  isEditable: boolean;
}

function formatDate(dateString?: string): string {
  if (!dateString) return "—";
  try {
    const date = new Date(dateString);
    return date.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return "—";
  }
}

export function WorkflowInfoPopover({
  isOpen,
  onClose,
  description,
  onDescriptionChange,
  createdAt,
  isEditable,
}: WorkflowInfoPopoverProps) {
  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Workflow Info" size="sm">
      <div className="space-y-4">
        {/* Description */}
        <div>
          <label className="block text-sm font-medium text-muted-foreground mb-2">
            Description
          </label>
          {isEditable && onDescriptionChange ? (
            <Textarea
              value={description}
              onChange={(e) => onDescriptionChange(e.target.value)}
              placeholder="Add a description for this workflow..."
              className="resize-none"
              rows={3}
            />
          ) : (
            <p className="text-sm text-foreground">
              {description || (
                <span className="text-muted-foreground italic">
                  No description
                </span>
              )}
            </p>
          )}
        </div>

        {/* Metadata Grid */}
        <div className="grid grid-cols-2 gap-4 pt-2 border-t border-border">
          {/* Created Date */}
          {createdAt && (
            <div>
              <label className="block text-xs font-medium text-muted-foreground mb-1.5">
                Created
              </label>
              <div className="flex items-center gap-1.5 text-sm text-foreground">
                <Calendar className="w-4 h-4 text-muted-foreground" />
                <span>{formatDate(createdAt)}</span>
              </div>
            </div>
          )}

        </div>

        {/* Read-only notice */}
        {!isEditable && (
          <p className="text-xs text-muted-foreground flex items-center gap-1.5 pt-2 border-t border-border">
            <Lock className="w-3 h-3" />
            This workflow is read-only
          </p>
        )}
      </div>
    </Modal>
  );
}
