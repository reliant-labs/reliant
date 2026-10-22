import { RefreshCw, GitCommit, CheckCircle2 } from "lucide-react";
import { Modal } from "../ui/Modal";

interface RescanModalProps {
  isOpen: boolean;
  projectName: string;
  commitCount: number;
  onConfirm: () => void;
  onCancel: () => void;
  onDismissForever?: () => void;
}

export function RescanModal({
  isOpen,
  projectName,
  commitCount,
  onConfirm,
  onCancel,
  onDismissForever,
}: RescanModalProps) {
  return (
    <Modal 
      isOpen={isOpen} 
      onClose={onCancel} 
      title="Project Rescan Recommended"
      size="md"
    >
      <div className="space-y-6">
        <div className="flex items-start gap-4">
          <div className="p-3 rounded-xl bg-primary/10 ring-1 ring-primary/20">
            <RefreshCw className="w-6 h-6 text-primary" />
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2 mb-3">
              <GitCommit className="w-4 h-4 text-muted-foreground" />
              <p className="text-sm text-foreground">
                <span className="font-semibold text-primary">{commitCount} commits</span> since 
                last scan of <span className="font-semibold text-primary">{projectName}</span>
              </p>
            </div>
            
            <p className="text-sm text-muted-foreground leading-relaxed">
              Rescanning will update the project metadata, testing infrastructure, and AI understanding of your codebase 
              to reflect recent changes.
            </p>
          </div>
        </div>

        <div className="p-4 bg-muted/30 rounded-lg border border-border space-y-3">
          <p className="text-xs font-semibold text-foreground uppercase tracking-wide">
            What happens during a rescan:
          </p>
          <ul className="text-sm text-muted-foreground space-y-2">
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span>Analyzes new code and structural changes</span>
            </li>
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span>Updates LLM testing scenarios</span>
            </li>
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span>Refreshes project-meta.json</span>
            </li>
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span>Adjusts debugging strategies</span>
            </li>
          </ul>
        </div>

        <div className="flex items-center justify-between pt-4 border-t border-border">
          {onDismissForever && (
            <button
              onClick={onDismissForever}
              className="px-3 py-2 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background rounded-lg"
            >
              Don't ask again
            </button>
          )}
          <div className="flex gap-3 ml-auto">
            <button
              onClick={onCancel}
              className="px-5 py-2.5 text-sm font-medium text-foreground bg-muted hover:bg-muted/80 border border-border rounded-lg transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
            >
              Later
            </button>
            <button
              onClick={onConfirm}
              className="px-5 py-2.5 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
            >
              Rescan Now
            </button>
          </div>
        </div>
      </div>
    </Modal>
  );
}