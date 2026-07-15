// Copyright (c) 2025 Reliant Labs

/**
 * File type classification for attachments.
 * Must be kept in sync with internal/attachment/filetypes.go
 */

export type AttachmentType = 'image' | 'document' | 'file_reference' | 'unsupported';

// Image extensions supported by Claude API (base64 image blocks)
export const IMAGE_EXTENSIONS = new Set([
  '.jpg',
  '.jpeg',
  '.png',
  '.gif',
  '.webp',
]);

// Binary document extensions sent to the LLM natively rather than as extracted
// text. PDFs are read on demand (and paginated) via the read_attachment tool.
export const DOCUMENT_EXTENSIONS = new Set([
  '.pdf',
]);

// Text file extensions that can be read and sent as text content
export const TEXT_EXTENSIONS = new Set([
  // Markdown and documentation
  '.md', '.markdown', '.mdx', '.txt', '.text', '.rst',

  // Data formats
  '.json', '.yaml', '.yml', '.toml', '.xml', '.csv', '.tsv',
  '.ini', '.conf', '.cfg', '.env', '.properties', '.docx',

  // Programming languages
  '.go', '.py', '.js', '.jsx', '.ts', '.tsx', '.rs', '.java', '.kt', '.scala',
  '.c', '.cpp', '.cc', '.cxx', '.h', '.hpp', '.hxx', '.cs', '.swift',
  '.rb', '.php', '.pl', '.pm', '.lua', '.r', '.R', '.m', '.mm',
  '.zig', '.nim', '.v', '.d', '.dart', '.ex', '.exs', '.erl', '.hrl',
  '.clj', '.cljs', '.cljc', '.fs', '.fsx', '.ml', '.mli', '.hs', '.lhs',
  '.elm', '.purs', '.jl',

  // Shell and scripts
  '.sh', '.bash', '.zsh', '.fish', '.ps1', '.psm1', '.bat', '.cmd',

  // Web
  '.html', '.htm', '.css', '.scss', '.sass', '.less', '.vue', '.svelte', '.astro',

  // Config and build
  '.dockerfile', '.makefile', '.cmake', '.gradle', '.sbt', '.cabal', '.gemspec', '.podspec',

  // SQL and queries
  '.sql', '.graphql', '.gql',

  // Other text formats
  '.log', '.diff', '.patch', '.proto', '.thrift', '.avsc', '.tf', '.tfvars', '.hcl',
]);

// Special filenames that are text files regardless of extension
export const TEXT_FILENAMES = new Set([
  'Dockerfile', 'Makefile', 'CMakeLists.txt', 'Gemfile', 'Rakefile',
  'Vagrantfile', 'Procfile', 'Brewfile',
  '.gitignore', '.gitattributes', '.dockerignore', '.editorconfig',
  '.prettierrc', '.eslintrc', '.babelrc', '.npmrc', '.nvmrc', '.yarnrc',
  'LICENSE', 'README', 'CHANGELOG', 'AUTHORS', 'CONTRIBUTORS', 'COPYING', 'NOTICE', 'VERSION',
]);

/**
 * Get the file extension from a filename (lowercase)
 */
function getExtension(filename: string): string {
  const lastDot = filename.lastIndexOf('.');
  if (lastDot === -1 || lastDot === filename.length - 1) {
    return '';
  }
  return filename.slice(lastDot).toLowerCase();
}

/**
 * Get the basename from a path
 */
function getBasename(filepath: string): string {
  const lastSlash = Math.max(filepath.lastIndexOf('/'), filepath.lastIndexOf('\\'));
  return lastSlash === -1 ? filepath : filepath.slice(lastSlash + 1);
}

/**
 * Determine how a file should be handled based on its name
 */
export function getAttachmentType(filename: string): AttachmentType {
  const basename = getBasename(filename);
  
  // Check special filenames first
  if (TEXT_FILENAMES.has(basename)) {
    return 'file_reference';
  }

  const ext = getExtension(filename);
  if (!ext) {
    return 'unsupported';
  }

  // Check if it's an image
  if (IMAGE_EXTENSIONS.has(ext)) {
    return 'image';
  }

  // Check if it's a binary document (PDF) sent natively / read on demand
  if (DOCUMENT_EXTENSIONS.has(ext)) {
    return 'document';
  }

  // Check if it's a text file
  if (TEXT_EXTENSIONS.has(ext)) {
    return 'file_reference';
  }

  return 'unsupported';
}

/**
 * Check if a file type is supported for attachment
 */
export function isFileTypeSupported(filename: string): boolean {
  return getAttachmentType(filename) !== 'unsupported';
}

/**
 * Check if a file is an image type
 */
export function isImageFile(filename: string): boolean {
  return getAttachmentType(filename) === 'image';
}

/**
 * Check if a file is a text/file reference type
 */
export function isTextFile(filename: string): boolean {
  return getAttachmentType(filename) === 'file_reference';
}

/**
 * Get a human-readable error message for unsupported file types
 */
export function getUnsupportedFileMessage(filename: string): string {
  const ext = getExtension(filename);
  if (!ext) {
    return `Files without extensions are not supported. Supported: images (jpg, png, gif, webp) and text files (md, txt, json, code files, etc.)`;
  }
  return `File type "${ext}" is not supported. Supported: images (jpg, png, gif, webp) and text files (md, txt, json, code files, etc.)`;
}

/**
 * Get MIME types for the file input accept attribute
 */
export function getAcceptedMimeTypes(): string {
  // Images
  const imageMimes = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'];
  
  // Text - use wildcard for text/* and common extensions
  const textMimes = [
    'text/*',
    'application/json',
    'application/xml',
    'application/x-yaml',
    'application/pdf',
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  ];
  
  // Also include common extensions for file picker
  const extensions = [
    ...Array.from(IMAGE_EXTENSIONS),
    ...Array.from(DOCUMENT_EXTENSIONS),
    ...Array.from(TEXT_EXTENSIONS),
  ];

  return [...imageMimes, ...textMimes, ...extensions].join(',');
}