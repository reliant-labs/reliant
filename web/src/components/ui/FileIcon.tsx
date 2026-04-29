import { FileText, Image, FileCode, Table, File, Database, Terminal, FileCog } from 'lucide-react';

interface FileIconProps {
  mimeType?: string;
  fileName?: string;
  className?: string;
}

// File type categories for icon selection
type FileCategory = 'code' | 'config' | 'data' | 'image' | 'document' | 'script' | 'database' | 'generic';

function getFileCategoryFromFileName(fileName: string): FileCategory {
  const ext = fileName.split('.').pop()?.toLowerCase() || '';
  const baseName = fileName.split('/').pop()?.toLowerCase() || '';
  
  // Check for special filenames first
  const specialFiles: Record<string, FileCategory> = {
    'dockerfile': 'config',
    'makefile': 'script',
    'gemfile': 'config',
    'rakefile': 'script',
    'procfile': 'config',
    '.gitignore': 'config',
    '.gitattributes': 'config',
    '.dockerignore': 'config',
    '.editorconfig': 'config',
    '.prettierrc': 'config',
    '.eslintrc': 'config',
    '.babelrc': 'config',
  };
  
  if (specialFiles[baseName]) return specialFiles[baseName];
  
  const categoryMap: Record<string, FileCategory> = {
    // Code - Programming languages
    'js': 'code', 'jsx': 'code', 'ts': 'code', 'tsx': 'code', 'mjs': 'code', 'cjs': 'code',
    'py': 'code', 'pyw': 'code', 'pyx': 'code',
    'go': 'code', 'mod': 'config',
    'rs': 'code', 'rlib': 'code',
    'java': 'code', 'kt': 'code', 'kts': 'code', 'scala': 'code', 'groovy': 'code',
    'c': 'code', 'h': 'code', 'cpp': 'code', 'hpp': 'code', 'cc': 'code', 'cxx': 'code',
    'cs': 'code', 'fs': 'code', 'vb': 'code',
    'swift': 'code', 'm': 'code', 'mm': 'code',
    'rb': 'code', 'erb': 'code',
    'php': 'code', 'phtml': 'code',
    'pl': 'code', 'pm': 'code',
    'lua': 'code', 'vim': 'code',
    'r': 'code', 'R': 'code',
    'dart': 'code', 'elm': 'code', 'ex': 'code', 'exs': 'code',
    'clj': 'code', 'cljs': 'code', 'edn': 'config',
    'hs': 'code', 'lhs': 'code', 'ml': 'code', 'mli': 'code',
    'vue': 'code', 'svelte': 'code', 'astro': 'code',
    // Web
    'html': 'code', 'htm': 'code', 'xhtml': 'code',
    'css': 'code', 'scss': 'code', 'sass': 'code', 'less': 'code', 'styl': 'code',
    // Config files
    'json': 'config', 'jsonc': 'config', 'json5': 'config',
    'yaml': 'config', 'yml': 'config',
    'toml': 'config', 'ini': 'config', 'cfg': 'config', 'conf': 'config',
    'xml': 'config', 'xsl': 'config', 'xslt': 'config',
    'env': 'config', 'properties': 'config',
    'lock': 'config', 'sum': 'config',
    // Data files
    'csv': 'data', 'tsv': 'data',
    'xlsx': 'data', 'xls': 'data', 'ods': 'data',
    // Database
    'sql': 'database', 'sqlite': 'database', 'db': 'database',
    'prisma': 'database',
    // Documents
    'md': 'document', 'mdx': 'document', 'markdown': 'document',
    'txt': 'document', 'rtf': 'document',
    'pdf': 'document', 'doc': 'document', 'docx': 'document',
    'rst': 'document', 'adoc': 'document', 'asciidoc': 'document',
    // Scripts
    'sh': 'script', 'bash': 'script', 'zsh': 'script', 'fish': 'script',
    'ps1': 'script', 'psm1': 'script', 'bat': 'script', 'cmd': 'script',
    // Images
    'jpg': 'image', 'jpeg': 'image', 'png': 'image', 'gif': 'image',
    'svg': 'image', 'webp': 'image', 'ico': 'image', 'bmp': 'image',
    'avif': 'image', 'heic': 'image', 'heif': 'image',
  };
  
  return categoryMap[ext] || 'generic';
}

export function FileIcon({ mimeType, fileName, className = "w-4 h-4" }: FileIconProps) {
  const iconProps = { className, "aria-hidden": true };

  // If mimeType is explicitly provided, use it for images
  if (mimeType?.startsWith('image/')) return <Image {...iconProps} />;

  // Use file category based on extension/filename
  if (fileName) {
    const category = getFileCategoryFromFileName(fileName);
    
    switch (category) {
      case 'code': return <FileCode {...iconProps} />;
      case 'config': return <FileCog {...iconProps} />;
      case 'data': return <Table {...iconProps} />;
      case 'image': return <Image {...iconProps} />;
      case 'document': return <FileText {...iconProps} />;
      case 'script': return <Terminal {...iconProps} />;
      case 'database': return <Database {...iconProps} />;
      default: return <File {...iconProps} />;
    }
  }

  return <File {...iconProps} />;
}