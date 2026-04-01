/**
 * Utility functions for file handling
 */

const BINARY_EXTENSIONS = new Set([
  // Databases
  '.db', '.sqlite', '.sqlite3', '.db-shm', '.db-wal',
  // Executables
  '.exe', '.dll', '.so', '.dylib', '.bin', '.dat',
  // Documents
  '.pdf', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx',
  // Archives
  '.zip', '.tar', '.gz', '.bz2', '.7z', '.rar',
  // Audio/Video
  '.mp3', '.mp4', '.avi', '.mov', '.wmv', '.flv', '.wav',
  // Fonts
  '.woff', '.woff2', '.ttf', '.otf', '.eot',
  // Compiled/Object files
  '.class', '.pyc', '.pyo', '.o', '.a', '.lib',
]);

// System and metadata files that should not be opened
const SYSTEM_FILES = new Set([
  '.ds_store',
  'thumbs.db',
  'desktop.ini',
  '.localized',
]);

const IMAGE_EXTENSIONS = new Set([
  '.jpg', '.jpeg', '.png', '.gif', '.bmp', '.ico', '.tiff', '.webp',
]);

/**
 * Check if a file is an image based on its extension
 */
export function isImageFile(filename: string): boolean {
  const ext = filename.toLowerCase().match(/\.[^.]+$/)?.[0];
  return ext ? IMAGE_EXTENSIONS.has(ext) : false;
}

/**
 * Check if a file is likely binary or a system file based on its name/extension
 */
export function isBinaryFile(filename: string): boolean {
  const lowerFilename = filename.toLowerCase();
  
  // Check if it's a system file (by exact filename)
  if (SYSTEM_FILES.has(lowerFilename)) {
    return true;
  }
  
  // Check if it's a binary file (by extension)
  const ext = lowerFilename.match(/\.[^.]+$/)?.[0];
  return ext ? BINARY_EXTENSIONS.has(ext) : false;
}

/**
 * Format file size in human-readable format
 */
export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
}

/**
 * Get file extension from filename
 */
export function getFileExtension(filename: string): string {
  const ext = filename.toLowerCase().match(/\.[^.]+$/)?.[0];
  return ext ? ext.slice(1) : '';
}

/**
 * Build the full filesystem path by combining worktree path with file's relative path.
 * 
 * @param worktreePath - The full filesystem path to the worktree (e.g., /Users/.../worktrees/abc123/feature-branch)
 * @param relativePath - The file path relative to the project root (e.g., /.env or /src/App.tsx)
 * @returns The full filesystem path (e.g., /Users/.../worktrees/abc123/feature-branch/.env)
 */
export function buildFullPath(worktreePath: string | undefined, relativePath: string): string {
  if (!worktreePath) {
    return relativePath;
  }
  
  // Normalize the relative path - remove leading slash if present since worktreePath doesn't end with one
  const normalizedRelative = relativePath.startsWith('/') ? relativePath : '/' + relativePath;
  
  // Remove trailing slash from worktree path if present
  const normalizedWorktree = worktreePath.endsWith('/') ? worktreePath.slice(0, -1) : worktreePath;
  
  return normalizedWorktree + normalizedRelative;
}

/**
 * Get the workspace-relative path from an absolute file path.
 * Strips the workspace root path prefix to show a clean relative path.
 * 
 * @param absolutePath - The full filesystem path (e.g., /Users/.../landing-page/src/App.tsx)
 * @param workspacePath - The workspace root path (e.g., /Users/.../landing-page)
 * @returns The relative path without leading slash (e.g., src/App.tsx)
 */
export function getRelativePath(absolutePath: string, workspacePath: string | undefined): string {
  if (!workspacePath) {
    // Strip leading slash if present
    return absolutePath.startsWith('/') ? absolutePath.slice(1) : absolutePath;
  }
  
  // Normalize workspace path - remove trailing slash
  const normalizedWorkspace = workspacePath.endsWith('/') 
    ? workspacePath.slice(0, -1) 
    : workspacePath;
  
  // If the absolute path starts with the workspace path, strip it
  if (absolutePath.startsWith(normalizedWorkspace)) {
    const relativePath = absolutePath.slice(normalizedWorkspace.length);
    // Remove leading slash for cleaner display
    return relativePath.startsWith('/') ? relativePath.slice(1) : relativePath;
  }
  
  // If not under workspace, return as-is (but still strip leading slash)
  return absolutePath.startsWith('/') ? absolutePath.slice(1) : absolutePath;
}
