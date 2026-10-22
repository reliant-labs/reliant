/**
 * Git diff parser that reconstructs full file content with change tracking
 */

export interface DiffLine {
  type: "added" | "removed" | "unchanged" | "context";
  oldLineNumber?: number;
  newLineNumber?: number;
  content: string;
}

export interface ParsedDiff {
  oldFileName: string;
  newFileName: string;
  isNewFile: boolean;
  isDeletedFile: boolean;
  lines: DiffLine[];
}

/**
 * Parse a unified git diff into structured format with full file content
 * Supports both unified diffs (git diff output) and simple content comparison
 */
export function parseUnifiedDiff(diffText: string): ParsedDiff {
  const lines = diffText.split("\n");
  const result: ParsedDiff = {
    oldFileName: "",
    newFileName: "",
    isNewFile: false,
    isDeletedFile: false,
    lines: [],
  };

  let oldLineNum = 0;
  let newLineNum = 0;
  let inHunk = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    // Parse file headers
    if (line.startsWith("--- ")) {
      result.oldFileName = line.substring(4).replace(/^a\//, "");
      if (line.includes("/dev/null")) {
        result.isNewFile = true;
      }
      continue;
    }

    if (line.startsWith("+++ ")) {
      result.newFileName = line.substring(4).replace(/^b\//, "");
      if (line.includes("/dev/null")) {
        result.isDeletedFile = true;
      }
      continue;
    }

    // Parse hunk headers (@@ -old_start,old_count +new_start,new_count @@)
    if (line.startsWith("@@ ")) {
      const match = line.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
      if (match) {
        oldLineNum = parseInt(match[1], 10);
        newLineNum = parseInt(match[2], 10);
        inHunk = true;
      }
      continue;
    }

    // Skip diff metadata lines
    if (
      line.startsWith("diff --git") ||
      line.startsWith("index ") ||
      line.startsWith("new file") ||
      line.startsWith("deleted file") ||
      line.startsWith("similarity index") ||
      line.startsWith("rename from") ||
      line.startsWith("rename to")
    ) {
      continue;
    }

    if (!inHunk) continue;

    // Parse diff lines
    if (line.startsWith("+")) {
      result.lines.push({
        type: "added",
        newLineNumber: newLineNum++,
        content: line.substring(1),
      });
    } else if (line.startsWith("-")) {
      result.lines.push({
        type: "removed",
        oldLineNumber: oldLineNum++,
        content: line.substring(1),
      });
    } else if (line.startsWith(" ") || line === "") {
      // Context line (unchanged)
      result.lines.push({
        type: "context",
        oldLineNumber: oldLineNum++,
        newLineNumber: newLineNum++,
        content: line.substring(1),
      });
    }
  }

  return result;
}

/**
 * Reconstruct full file content from a diff
 * This creates a complete view of the new file with all lines
 */
export function reconstructFullFile(
  diffText: string,
  originalContent?: string
): DiffLine[] {
  // If we have original content, use it to build full context
  if (originalContent !== undefined && originalContent !== null) {
    return reconstructFromOriginal(diffText, originalContent);
  }

  // Otherwise parse just the diff
  const parsed = parseUnifiedDiff(diffText);
  return parsed.lines;
}

/**
 * Reconstruct file with full context using original content
 */
function reconstructFromOriginal(
  diffText: string,
  originalContent: string
): DiffLine[] {
  const parsed = parseUnifiedDiff(diffText);
  const originalLines = originalContent.split("\n");
  const result: DiffLine[] = [];

  // Build a map of changes by line number
  const changesByOldLine = new Map<number, DiffLine[]>();
  const changesByNewLine = new Map<number, DiffLine[]>();

  for (const diffLine of parsed.lines) {
    if (diffLine.type === "removed" && diffLine.oldLineNumber) {
      if (!changesByOldLine.has(diffLine.oldLineNumber)) {
        changesByOldLine.set(diffLine.oldLineNumber, []);
      }
      changesByOldLine.get(diffLine.oldLineNumber)!.push(diffLine);
    } else if (diffLine.type === "added" && diffLine.newLineNumber) {
      if (!changesByNewLine.has(diffLine.newLineNumber)) {
        changesByNewLine.set(diffLine.newLineNumber, []);
      }
      changesByNewLine.get(diffLine.newLineNumber)!.push(diffLine);
    }
  }

  // Build result with all original lines plus changes
  let newLineNum = 1;
  for (let i = 0; i < originalLines.length; i++) {
    const oldLineNum = i + 1;
    const originalLine = originalLines[i];

    // Check if this line was removed or modified
    if (changesByOldLine.has(oldLineNum)) {
      const changes = changesByOldLine.get(oldLineNum)!;
      for (const change of changes) {
        result.push(change);
      }
      // Check if there's a corresponding addition (modification)
      if (changesByNewLine.has(newLineNum)) {
        const additions = changesByNewLine.get(newLineNum)!;
        for (const addition of additions) {
          result.push(addition);
        }
        changesByNewLine.delete(newLineNum);
        newLineNum++;
      }
    }
    // Check if this line has additions before it
    else if (changesByNewLine.has(newLineNum)) {
      const additions = changesByNewLine.get(newLineNum)!;
      for (const addition of additions) {
        result.push(addition);
      }
      changesByNewLine.delete(newLineNum);
      newLineNum++;

      // Then add the unchanged line
      result.push({
        type: "unchanged",
        oldLineNumber: oldLineNum,
        newLineNumber: newLineNum++,
        content: originalLine,
      });
    }
    // Unchanged line
    else {
      result.push({
        type: "unchanged",
        oldLineNumber: oldLineNum,
        newLineNumber: newLineNum++,
        content: originalLine,
      });
    }
  }

  // Add any remaining additions at the end
  const remainingNewLines = Array.from(changesByNewLine.entries()).sort(
    (a, b) => a[0] - b[0]
  );
  for (const [, additions] of remainingNewLines) {
    for (const addition of additions) {
      result.push(addition);
    }
  }

  return result;
}

/**
 * Simple line-by-line comparison when we don't have a git diff
 */
export function compareContent(
  originalContent: string,
  modifiedContent: string
): DiffLine[] {
  const originalLines = originalContent.split("\n");
  const modifiedLines = modifiedContent.split("\n");
  const result: DiffLine[] = [];

  const maxLines = Math.max(originalLines.length, modifiedLines.length);
  let oldLineNum = 1;
  let newLineNum = 1;

  for (let i = 0; i < maxLines; i++) {
    const oldLine = originalLines[i];
    const newLine = modifiedLines[i];

    if (oldLine === undefined) {
      // Added line
      result.push({
        type: "added",
        newLineNumber: newLineNum++,
        content: newLine || "",
      });
    } else if (newLine === undefined) {
      // Removed line
      result.push({
        type: "removed",
        oldLineNumber: oldLineNum++,
        content: oldLine || "",
      });
    } else if (oldLine === newLine) {
      // Unchanged line
      result.push({
        type: "unchanged",
        oldLineNumber: oldLineNum++,
        newLineNumber: newLineNum++,
        content: oldLine,
      });
    } else {
      // Modified line - show as remove + add
      result.push({
        type: "removed",
        oldLineNumber: oldLineNum++,
        content: oldLine,
      });
      result.push({
        type: "added",
        newLineNumber: newLineNum++,
        content: newLine,
      });
    }
  }

  return result;
}

/**
 * Main entry point: intelligently parse diff or compare content
 */
export function parseDiff(
  diffOrOriginal: string,
  modifiedContent?: string
): DiffLine[] {
  // If we have both arguments, it's a simple comparison
  if (modifiedContent !== undefined) {
    return compareContent(diffOrOriginal, modifiedContent);
  }

  // Check if input looks like a unified diff
  if (
    diffOrOriginal.includes("--- ") &&
    diffOrOriginal.includes("+++ ") &&
    diffOrOriginal.includes("@@ ")
  ) {
    return parseUnifiedDiff(diffOrOriginal).lines;
  }

  // Otherwise treat as new file content
  return diffOrOriginal.split("\n").map((line, i) => ({
    type: "added" as const,
    newLineNumber: i + 1,
    content: line,
  }));
}
