import { useState, useCallback, useRef, type DragEvent } from 'react';
import { toast } from '../lib/toast-manager';

interface DragAndDropOptions {
  onDrop: (files: File[]) => Promise<void>;
  allowedMimeTypes: string[];
  maxFileSize?: number;
  maxFiles?: number;
  disabled?: boolean;
}

interface DragAndDropState {
  isDragging: boolean;
  dragCounter: number;
  isValidDrag: boolean;
}

const ALLOWED_MIME_TYPES = [
  'image/jpeg', 'image/png', 'image/gif', 'image/webp', 'image/bmp', 'image/svg+xml',
  'text/plain', 'text/csv', 'text/markdown',
  'application/json', 'application/pdf',
  'application/msword', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.ms-excel', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
];

const FILE_TYPE_EXTENSIONS: Record<string, string[]> = {
  'image/jpeg': ['.jpg', '.jpeg'],
  'image/png': ['.png'],
  'image/gif': ['.gif'],
  'image/webp': ['.webp'],
  'image/bmp': ['.bmp'],
  'image/svg+xml': ['.svg'],
  'text/plain': ['.txt'],
  'text/csv': ['.csv'],
  'text/markdown': ['.md'],
  'application/json': ['.json'],
  'application/pdf': ['.pdf'],
  'application/msword': ['.doc'],
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': ['.docx'],
  'application/vnd.ms-excel': ['.xls'],
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': ['.xlsx'],
};

export function useDragAndDrop({
  onDrop,
  allowedMimeTypes = ALLOWED_MIME_TYPES,
  maxFileSize = 50 * 1024 * 1024, // 50MB default
  maxFiles = 10,
  disabled = false,
}: DragAndDropOptions) {
  const [state, setState] = useState<DragAndDropState>({
    isDragging: false,
    dragCounter: 0,
    isValidDrag: true,
  });

  const dragCounterRef = useRef(0);

  const validateFile = useCallback((file: File): { valid: boolean; error?: string } => {
    // Check file size
    if (file.size > maxFileSize) {
      const sizeMB = Math.round(maxFileSize / (1024 * 1024));
      return {
        valid: false,
        error: `File "${file.name}" is too large (max ${sizeMB}MB)`
      };
    }

    // Check mime type - try to determine from both file.type and extension
    let mimeType = file.type;

    // If browser didn't provide mime type, try to detect from extension
    if (!mimeType || mimeType === '') {
      const extension = '.' + file.name.split('.').pop()?.toLowerCase();
      for (const [mime, extensions] of Object.entries(FILE_TYPE_EXTENSIONS)) {
        if (extensions.includes(extension)) {
          mimeType = mime;
          break;
        }
      }
    }

    // Check if mime type is allowed
    const isAllowed = allowedMimeTypes.some(allowed =>
      mimeType && mimeType.startsWith(allowed)
    );

    if (!isAllowed) {
      const fileExtension = file.name.split('.').pop()?.toLowerCase() || 'unknown';
      return {
        valid: false,
        error: `File type .${fileExtension} is not allowed`
      };
    }

    return { valid: true };
  }, [allowedMimeTypes, maxFileSize]);

  const processFiles = useCallback(async (files: FileList | File[]) => {
    if (disabled) return;

    const fileArray = Array.from(files);

    // Check max files limit
    if (fileArray.length > maxFiles) {
      toast.error(`Too many files. Maximum ${maxFiles} files allowed.`, {
        duration: 4000
      });
      return;
    }

    // Validate all files
    const validFiles: File[] = [];
    const errors: string[] = [];

    for (const file of fileArray) {
      const validation = validateFile(file);
      if (validation.valid) {
        validFiles.push(file);
      } else if (validation.error) {
        errors.push(validation.error);
      }
    }

    // Show errors for invalid files
    if (errors.length > 0) {
      errors.forEach(error => {
        toast.error(error, { duration: 5000 });
      });
    }

    // Process valid files
    if (validFiles.length > 0) {
      try {
        await onDrop(validFiles);
        // Success feedback is provided by the file appearing in the attachment preview
        // Don't show toast here - the onDrop handler may reject files with its own error toasts
      } catch (error) {
        console.error('Error processing dropped files:', error);
        toast.error('Failed to process files. Please try again.', {
          duration: 4000
        });
      }
    }
  }, [disabled, maxFiles, validateFile, onDrop]);

  const handleDragEnter = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    if (disabled) return;

    dragCounterRef.current++;

    if (e.dataTransfer?.items && e.dataTransfer.items.length > 0) {
      // Check if any of the dragged items are files
      let hasValidFiles = false;
      let hasInvalidFiles = false;

      for (let i = 0; i < e.dataTransfer.items.length; i++) {
        const item = e.dataTransfer.items[i];
        if (item.kind === 'file') {
          // Try to get file type from the item
          const type = item.type;
          if (type) {
            const isValid = allowedMimeTypes.some(allowed => type.startsWith(allowed));
            if (isValid) {
              hasValidFiles = true;
            } else {
              hasInvalidFiles = true;
            }
          } else {
            // If no type available during drag, assume it might be valid
            hasValidFiles = true;
          }
        }
      }

      setState(prev => ({
        ...prev,
        isDragging: true,
        dragCounter: dragCounterRef.current,
        isValidDrag: !hasInvalidFiles || hasValidFiles,
      }));
    }
  }, [disabled, allowedMimeTypes]);

  const handleDragLeave = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    if (disabled) return;

    dragCounterRef.current--;

    if (dragCounterRef.current === 0) {
      setState(prev => ({
        ...prev,
        isDragging: false,
        dragCounter: 0,
        isValidDrag: true,
      }));
    }
  }, [disabled]);

  const handleDragOver = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    if (disabled) return;

    // Set the drop effect to indicate this is a valid drop zone
    if (e.dataTransfer) {
      e.dataTransfer.dropEffect = 'copy';
    }
  }, [disabled]);

  const handleDrop = useCallback((e: DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    if (disabled) return;

    // Reset drag state
    dragCounterRef.current = 0;
    setState({
      isDragging: false,
      dragCounter: 0,
      isValidDrag: true,
    });

    // Get files from the drag event
    const files = e.dataTransfer?.files;
    if (files && files.length > 0) {
      processFiles(files);
    }
  }, [disabled, processFiles]);

  // Handle paste events for file pasting
  const handlePaste = useCallback((e: ClipboardEvent) => {
    if (disabled) return;

    const files = e.clipboardData?.files;
    if (files && files.length > 0) {
      e.preventDefault();
      processFiles(files);
    }
  }, [disabled, processFiles]);

  return {
    isDragging: state.isDragging,
    isValidDrag: state.isValidDrag,
    handlers: {
      onDragEnter: handleDragEnter,
      onDragLeave: handleDragLeave,
      onDragOver: handleDragOver,
      onDrop: handleDrop,
    },
    handlePaste,
    processFiles,
  };
}