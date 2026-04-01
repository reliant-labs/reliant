/**
 * FilePathRenderer - Component to detect and render file paths as clickable links
 */

import { Fragment } from 'react';
import { detectFilePaths } from '../../lib/filePath';
import { FileLink } from './FileLink';

interface FilePathRendererProps {
  text: string;
  className?: string;
  worktreeId?: string; // Worktree context for file links
}

/**
 * Renders text with detected file paths as clickable FileLink components
 */
export function FilePathRenderer({ text, className, worktreeId }: FilePathRendererProps) {
  const detections = detectFilePaths(text);
  
  // If no file paths detected, return plain text
  if (detections.length === 0) {
    return <span className={className}>{text}</span>;
  }

  // Build fragments with FileLinks interspersed
  const fragments: React.ReactNode[] = [];
  let lastIndex = 0;

  detections.forEach((detection, idx) => {
    // Add text before the file path
    if (detection.start > lastIndex) {
      fragments.push(
        <Fragment key={`text-${idx}`}>
          {text.substring(lastIndex, detection.start)}
        </Fragment>
      );
    }

    // Add FileLink for the detected path
    fragments.push(
      <FileLink
        key={`file-${idx}`}
        path={detection.parsed}
        worktreeId={worktreeId}
        inline
      />
    );

    lastIndex = detection.end;
  });

  // Add remaining text after last file path
  if (lastIndex < text.length) {
    fragments.push(
      <Fragment key="text-end">
        {text.substring(lastIndex)}
      </Fragment>
    );
  }

  return <span className={className}>{fragments}</span>;
}
