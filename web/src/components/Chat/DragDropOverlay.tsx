import { Upload,FileX } from 'lucide-react';

interface DragDropOverlayProps {
  isValid: boolean;
  isDragging: boolean;
}

export function DragDropOverlay({ isValid, isDragging }: DragDropOverlayProps) {
  if (!isDragging) return null;

  return (
    <div className="absolute inset-0 rounded-lg pointer-events-none flex items-center justify-center z-20">
      <div
        className="absolute inset-0 rounded-lg transition-all duration-200"
        style={{
          backgroundColor: isValid
            ? 'var(--transparent-button-hover)'
            : 'var(--destructive)',
          opacity: isValid ? 1 : 0.05,
        }}
      />
      <div
        className="relative flex flex-col items-center gap-2 px-6 py-4 rounded-lg transition-all duration-200 transform"
        style={{
          backgroundColor: isValid
            ? 'var(--chat-button-bg)'
            : 'var(--destructive)',
          color: isValid
            ? 'var(--chat-input-text)'
            : 'var(--destructive-foreground)',
          border: `1px solid ${isValid ? 'var(--chat-border)' : 'var(--destructive)'}`,
          opacity: isValid ? 1 : 0.9,
          boxShadow: '0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06)',
        }}
      >
        {isValid ? (
          <>
            <Upload className="w-8 h-8" style={{ color: 'var(--chat-input-text)' }} />
            <span className="font-medium">Drop files to attach</span>
            <span className="text-xs opacity-75">Images, PDFs, documents, and text files</span>
          </>
        ) : (
          <>
            <FileX className="w-8 h-8" />
            <span className="font-medium">Invalid file type</span>
            <span className="text-xs opacity-90">Only specific file types are allowed</span>
          </>
        )}
      </div>
    </div>
  );
}