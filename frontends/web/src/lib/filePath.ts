/**
 * File path detection and parsing utilities
 */

export interface ParsedFilePath {
  fullPath: string;
  path: string;
  line?: number;
  lineEnd?: number; // For line ranges like :10:50
  column?: number;
  isAbsolute: boolean;
}

/**
 * Regular expression patterns for detecting file paths
 */
const FILE_PATH_PATTERNS = {
  // Absolute path with optional line:column (e.g., /path/to/file.ts:123:45)
  // Matches paths starting with / followed by one or more path segments
  absoluteWithLocation: /\/(?:[a-zA-Z0-9_.-]+\/)*[a-zA-Z0-9_.-]+\.[a-zA-Z0-9]+(?::\d+)?(?::\d+)?/g,
  
  // Relative path with ./ or ../ prefix (e.g., ./src/file.ts:123, ../file.ts)
  relativeWithPrefix: /\.{1,2}\/(?:[a-zA-Z0-9_.-]+\/)*[a-zA-Z0-9_.-]+\.[a-zA-Z0-9]+(?::\d+)?(?::\d+)?/g,
};

/**
 * Common file extensions to validate detected paths
 */
const VALID_EXTENSIONS = new Set([
  // Code files
  'ts', 'tsx', 'js', 'jsx', 'py', 'go', 'rs', 'java', 'c', 'cpp', 'h', 'hpp',
  'css', 'scss', 'sass', 'less', 'html', 'xml', 'json', 'yaml', 'yml', 'toml',
  'sh', 'bash', 'zsh', 'fish', 'ps1', 'bat', 'cmd',
  'sql', 'graphql', 'proto', 'vue', 'svelte', 'astro',
  // Documentation
  'md', 'mdx', 'txt', 'rst', 'adoc',
  // Config files
  'env', 'ini', 'conf', 'config', 'lock',
  // Build/package files
  'makefile', 'dockerfile', 'gitignore', 'editorconfig',
]);

/**
 * Parse a file path string into its components
 */
export function parseFilePath(pathString: string): ParsedFilePath | null {
  if (!pathString || typeof pathString !== 'string') {
    return null;
  }

  const trimmed = pathString.trim();
  if (!trimmed) {
    return null;
  }

  // Extract path and location info
  const parts = trimmed.split(':');
  const path = parts[0];
  const line = parts[1] ? parseInt(parts[1], 10) : undefined;
  
  // parts[2] could be either column or end line for a range
  // If we have 3 parts and both are numbers, treat as line:lineEnd
  // If we have 4 parts, treat as line:column or line:lineEnd depending on context
  let lineEnd: number | undefined;
  let column: number | undefined;
  
  if (parts[2]) {
    const secondNum = parseInt(parts[2], 10);
    if (!isNaN(secondNum)) {
      // Heuristic: if the second number is much larger than the first,
      // it's likely a line range (e.g., 10:50), otherwise it's a column (e.g., 10:5)
      if (line && secondNum > line && secondNum - line > 5) {
        lineEnd = secondNum;
      } else {
        column = secondNum;
      }
    }
  }

  // Validate extension - must have a valid file extension
  const ext = path.split('.').pop()?.toLowerCase();
  if (!ext || !VALID_EXTENSIONS.has(ext)) {
    return null;
  }

  // Validate it looks like a file path (not just random text with a dot)
  // Must contain at least one of: /, \, or be a simple filename.ext
  const looksLikeFilePath = 
    path.includes('/') || 
    path.includes('\\') || 
    /^[a-zA-Z0-9_.-]+\.[a-zA-Z0-9]+$/.test(path);
  
  if (!looksLikeFilePath) {
    return null;
  }

  // Check if absolute path
  const isAbsolute = path.startsWith('/');

  return {
    fullPath: trimmed,
    path,
    line: line && !isNaN(line) ? line : undefined,
    lineEnd: lineEnd && !isNaN(lineEnd) ? lineEnd : undefined,
    column: column && !isNaN(column) ? column : undefined,
    isAbsolute,
  };
}

/**
 * Detect file paths in text content
 * Returns array of matches with their positions
 */
export function detectFilePaths(text: string): Array<{ match: string; start: number; end: number; parsed: ParsedFilePath }> {
  if (!text || typeof text !== 'string') {
    return [];
  }

  const results: Array<{ match: string; start: number; end: number; parsed: ParsedFilePath }> = [];
  const seen = new Set<string>();

  // Try each pattern
  for (const pattern of Object.values(FILE_PATH_PATTERNS)) {
    pattern.lastIndex = 0; // Reset regex state
    let match;
    
    while ((match = pattern.exec(text)) !== null) {
      const fullMatch = match[0];
      const start = match.index;
      const end = start + fullMatch.length;
      
      // Avoid duplicates
      const key = `${start}-${end}-${fullMatch}`;
      if (seen.has(key)) {
        continue;
      }
      
      const parsed = parseFilePath(fullMatch);
      if (parsed) {
        results.push({
          match: fullMatch,
          start,
          end,
          parsed,
        });
        seen.add(key);
      }
    }
  }

  // Sort by position
  results.sort((a, b) => a.start - b.start);
  
  // Filter overlapping matches - when one match is contained within another,
  // keep the longer/more complete match. This handles cases like ./path where
  // the absolute pattern matches /path inside the relative match ./path
  const filtered = results.filter((result, i) => {
    return !results.some((other, j) => {
      if (i === j) return false;
      // Check if 'result' is fully contained within 'other'
      const resultContainedInOther = other.start <= result.start && other.end >= result.end;
      // If contained and other is longer (or same length but starts earlier), filter out result
      if (resultContainedInOther) {
        const otherIsLonger = (other.end - other.start) > (result.end - result.start);
        const sameLength = (other.end - other.start) === (result.end - result.start);
        return otherIsLonger || (sameLength && other.start < result.start);
      }
      return false;
    });
  });
  
  return filtered;
}

/**
 * Check if a string looks like a file path
 */
export function isFilePath(str: string): boolean {
  if (!str || typeof str !== 'string') {
    return false;
  }
  
  const parsed = parseFilePath(str);
  return parsed !== null;
}

/**
 * Normalize a file path for comparison
 */
export function normalizeFilePath(path: string): string {
  if (!path) {
    return '';
  }
  
  // Remove leading ./
  let normalized = path.replace(/^\.\//, '');
  
  // Ensure absolute paths start with /
  if (!normalized.startsWith('/') && !normalized.startsWith('.')) {
    normalized = '/' + normalized;
  }
  
  return normalized;
}
