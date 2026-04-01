/**
 * Monaco File Sync Service
 * 
 * Syncs project files to Monaco's virtual filesystem for cross-file intelligence.
 * This enables:
 * - Cross-file Go to Definition
 * - Import autocomplete
 * - Type information from other files
 * 
 * Uses lazy loading strategy to avoid loading entire project upfront.
 */

import type { Monaco } from '@monaco-editor/react';
import { getFileTree, getFileContent, getFilePreviewInfo } from '../api/fileSystem';
import { logger } from './logger';

// File extensions that should be synced to Monaco
const TYPESCRIPT_EXTENSIONS = ['.ts', '.tsx', '.d.ts'];
const JAVASCRIPT_EXTENSIONS = ['.js', '.jsx', '.mjs', '.cjs'];
const JSON_EXTENSIONS = ['.json'];
const ALL_SYNC_EXTENSIONS = [...TYPESCRIPT_EXTENSIONS, ...JAVASCRIPT_EXTENSIONS, ...JSON_EXTENSIONS];

// Directories to skip when scanning for files
const SKIP_DIRECTORIES = [
  'node_modules',
  '.git',
  'dist',
  'build',
  '.next',
  '.nuxt',
  'coverage',
  '.cache',
  '__pycache__',
  '.venv',
  'venv',
];

// Max file size to sync (1MB)
const MAX_FILE_SIZE = 1024 * 1024;

// Max files to sync initially
const MAX_INITIAL_FILES = 500;

interface SyncedFile {
  path: string;
  uri: string;
  content: string;
  version: number;
  lastSynced: number;
}

interface FileSyncConfig {
  worktreeId?: string;
  basePath?: string;
  maxFiles?: number;
  includeJson?: boolean;
}

class MonacoFileSyncService {
  private static instance: MonacoFileSyncService;
  private monaco: Monaco | null = null;
  private syncedFiles: Map<string, SyncedFile> = new Map();
  private pendingLoads: Map<string, Promise<void>> = new Map();
  private config: FileSyncConfig = {};
  private isInitialized = false;
  private isSyncing = false;

  private constructor() {}

  static getInstance(): MonacoFileSyncService {
    if (!MonacoFileSyncService.instance) {
      MonacoFileSyncService.instance = new MonacoFileSyncService();
    }
    return MonacoFileSyncService.instance;
  }

  /**
   * Initialize the file sync service with Monaco instance
   */
  initialize(monaco: Monaco, config?: FileSyncConfig): void {
    this.monaco = monaco;
    this.config = { maxFiles: MAX_INITIAL_FILES, includeJson: true, ...config };
    this.isInitialized = true;
    logger.info('[MonacoFileSync] Initialized', { config: this.config });
  }

  /**
   * Sync project files to Monaco's virtual filesystem
   * Uses lazy loading - only syncs declaration files and package.json initially
   */
  async syncProjectFiles(): Promise<void> {
    if (!this.monaco || !this.isInitialized) {
      logger.warn('[MonacoFileSync] Not initialized, skipping sync');
      return;
    }

    if (this.isSyncing) {
      logger.debug('[MonacoFileSync] Already syncing, skipping');
      return;
    }

    this.isSyncing = true;
    logger.info('[MonacoFileSync] Starting project file sync');

    try {
      // Get file tree
      const files = await this.collectTypeScriptFiles();
      logger.info(`[MonacoFileSync] Found ${files.length} files to potentially sync`);

      // Prioritize: .d.ts files first, then package.json, then source files
      const dtsFiles = files.filter(f => f.endsWith('.d.ts'));
      const packageJsonFiles = files.filter(f => f.endsWith('package.json'));
      const sourceFiles = files.filter(f => !f.endsWith('.d.ts') && !f.endsWith('package.json'));

      // Sync declaration files first (they're essential for type info)
      for (const filePath of dtsFiles.slice(0, 100)) {
        await this.syncFile(filePath);
      }

      // Sync package.json files (for import resolution)
      for (const filePath of packageJsonFiles.slice(0, 20)) {
        await this.syncFile(filePath);
      }

      // Sync some source files (lazy load the rest)
      const filesToSync = sourceFiles.slice(0, this.config.maxFiles || MAX_INITIAL_FILES);
      for (const filePath of filesToSync) {
        await this.syncFile(filePath);
      }

      logger.info(`[MonacoFileSync] Synced ${this.syncedFiles.size} files`);
    } catch (error) {
      logger.error('[MonacoFileSync] Failed to sync project files:', error);
    } finally {
      this.isSyncing = false;
    }
  }

  /**
   * Sync a single file to Monaco
   */
  async syncFile(filePath: string): Promise<void> {
    if (!this.monaco) return;

    // Check if already loading this file
    const pending = this.pendingLoads.get(filePath);
    if (pending) {
      return pending;
    }

    // Check if already synced and recent (within 5 minutes)
    const existing = this.syncedFiles.get(filePath);
    if (existing && Date.now() - existing.lastSynced < 5 * 60 * 1000) {
      return;
    }

    const loadPromise = this.doSyncFile(filePath);
    this.pendingLoads.set(filePath, loadPromise);

    try {
      await loadPromise;
    } finally {
      this.pendingLoads.delete(filePath);
    }
  }

  private async doSyncFile(filePath: string): Promise<void> {
    if (!this.monaco) return;

    try {
      const previewInfo = await getFilePreviewInfo(filePath, this.config.worktreeId);
      if (previewInfo.viewerKind !== 'text') {
        logger.debug(`[MonacoFileSync] Skipping non-text file: ${filePath}`);
        return;
      }

      const content = await getFileContent(filePath, this.config.worktreeId);
      
      // Skip large files
      if (content.length > MAX_FILE_SIZE) {
        logger.debug(`[MonacoFileSync] Skipping large file: ${filePath}`);
        return;
      }

      const uri = this.pathToUri(filePath);
      const existing = this.syncedFiles.get(filePath);
      const version = existing ? existing.version + 1 : 1;

      // Add to Monaco's virtual filesystem
      const isTypeScript = TYPESCRIPT_EXTENSIONS.some(ext => filePath.endsWith(ext));
      const isJavaScript = JAVASCRIPT_EXTENSIONS.some(ext => filePath.endsWith(ext));

      if (isTypeScript) {
        this.monaco.languages.typescript.typescriptDefaults.addExtraLib(content, uri);
      } else if (isJavaScript) {
        this.monaco.languages.typescript.javascriptDefaults.addExtraLib(content, uri);
      }

      // Track synced file (content omitted - Monaco retains it via addExtraLib)
      this.syncedFiles.set(filePath, {
        path: filePath,
        uri,
        content: '',
        version,
        lastSynced: Date.now(),
      });

      logger.debug(`[MonacoFileSync] Synced: ${filePath}`);
    } catch (error) {
      logger.debug(`[MonacoFileSync] Failed to sync ${filePath}:`, error);
    }
  }

  /**
   * Sync a file when it's opened in the editor
   * This ensures the file is in Monaco's virtual filesystem for Go to Definition
   */
  async syncFileOnOpen(filePath: string, content: string): Promise<void> {
    if (!this.monaco) return;

    const uri = this.pathToUri(filePath);
    const existing = this.syncedFiles.get(filePath);
    const version = existing ? existing.version + 1 : 1;

    const isTypeScript = TYPESCRIPT_EXTENSIONS.some(ext => filePath.endsWith(ext));
    const isJavaScript = JAVASCRIPT_EXTENSIONS.some(ext => filePath.endsWith(ext));

    if (isTypeScript) {
      this.monaco.languages.typescript.typescriptDefaults.addExtraLib(content, uri);
    } else if (isJavaScript) {
      this.monaco.languages.typescript.javascriptDefaults.addExtraLib(content, uri);
    }

    // Track synced file (content omitted - Monaco retains it via addExtraLib)
    this.syncedFiles.set(filePath, {
      path: filePath,
      uri,
      content: '',
      version,
      lastSynced: Date.now(),
    });

    // Also sync imports from this file
    await this.syncImportsFromFile(filePath, content);
  }

  /**
   * Parse imports from a file and sync those files
   */
  private async syncImportsFromFile(filePath: string, content: string): Promise<void> {
    // Extract imports using regex
    const importRegex = /(?:import|export)\s+(?:[\s\S]*?\s+from\s+)?['"]([^'"]+)['"]/g;
    const matches = content.matchAll(importRegex);

    const basePath = filePath.substring(0, filePath.lastIndexOf('/'));

    for (const match of matches) {
      const importPath = match[1];
      
      // Skip node_modules imports
      if (!importPath.startsWith('.') && !importPath.startsWith('/')) {
        continue;
      }

      // Resolve relative import
      const resolvedPath = this.resolveImportPath(basePath, importPath);
      if (resolvedPath && !this.syncedFiles.has(resolvedPath)) {
        // Sync in background, don't wait
        this.syncFile(resolvedPath).catch(() => {});
      }
    }
  }

  /**
   * Resolve an import path to an absolute file path
   */
  resolveImportPath(basePath: string, importPath: string): string | null {
    let resolvedPath: string;

    if (importPath.startsWith('/')) {
      resolvedPath = importPath;
    } else if (importPath.startsWith('.')) {
      // Resolve relative path
      const parts = basePath.split('/');
      const importParts = importPath.split('/');

      for (const part of importParts) {
        if (part === '..') {
          parts.pop();
        } else if (part !== '.') {
          parts.push(part);
        }
      }

      resolvedPath = parts.join('/');
    } else {
      // Node module import - skip for now
      return null;
    }

    // Try different extensions
    const extensions = ['', '.ts', '.tsx', '.js', '.jsx', '/index.ts', '/index.tsx', '/index.js', '/index.jsx'];
    
    for (const ext of extensions) {
      const fullPath = resolvedPath + ext;
      // We can't check if file exists without async call, so return the most likely one
      if (this.syncedFiles.has(fullPath)) {
        return fullPath;
      }
    }

    // Return with .ts extension as default guess for TypeScript projects
    if (!resolvedPath.includes('.')) {
      return resolvedPath + '.ts';
    }

    return resolvedPath;
  }

  /**
   * Collect TypeScript/JavaScript files from the project
   */
  private async collectTypeScriptFiles(): Promise<string[]> {
    const files: string[] = [];

    const traverse = async (path: string) => {
      try {
        const entries = await getFileTree(path, false, this.config.worktreeId);

        for (const entry of entries) {
          if (entry.type === 'directory') {
            // Skip certain directories
            const dirName = entry.name;
            if (SKIP_DIRECTORIES.includes(dirName)) {
              continue;
            }
            // Recursively traverse subdirectories
            await traverse(entry.path);
          } else if (entry.type === 'file') {
            // Check if file should be synced
            const shouldSync = ALL_SYNC_EXTENSIONS.some(ext => entry.name.endsWith(ext));
            if (shouldSync) {
              files.push(entry.path);
            }
          }

          // Stop if we've collected too many files
          if (files.length >= (this.config.maxFiles || MAX_INITIAL_FILES) * 2) {
            break;
          }
        }
      } catch (error) {
        logger.debug(`[MonacoFileSync] Failed to traverse ${path}:`, error);
      }
    };

    await traverse(this.config.basePath || '/');
    return files;
  }

  /**
   * Convert file path to Monaco URI
   */
  pathToUri(filePath: string): string {
    const normalized = filePath.replace(/\\/g, '/');
    if (normalized.startsWith('file:///')) {
      return normalized;
    }
    return `file:///${normalized.replace(/^\//, '')}`;
  }

  /**
   * Convert Monaco URI to file path
   */
  uriToPath(uri: string): string {
    return uri.replace('file:///', '/').replace('file://', '');
  }

  /**
   * Get synced file content by path
   */
  getSyncedFile(filePath: string): SyncedFile | undefined {
    return this.syncedFiles.get(filePath);
  }

  /**
   * Check if a file is synced
   */
  isFileSynced(filePath: string): boolean {
    return this.syncedFiles.has(filePath);
  }

  /**
   * Get all synced file paths
   */
  getSyncedFilePaths(): string[] {
    return Array.from(this.syncedFiles.keys());
  }

  /**
   * Get sync status
   */
  getStatus(): { initialized: boolean; syncing: boolean; fileCount: number } {
    return {
      initialized: this.isInitialized,
      syncing: this.isSyncing,
      fileCount: this.syncedFiles.size,
    };
  }

  /**
   * Clear all synced files
   */
  clear(): void {
    this.syncedFiles.clear();
    this.pendingLoads.clear();
    logger.info('[MonacoFileSync] Cleared all synced files');
  }

  /**
   * Update configuration
   */
  updateConfig(config: Partial<FileSyncConfig>): void {
    this.config = { ...this.config, ...config };
  }

  /**
   * Cleanup
   */
  dispose(): void {
    this.clear();
    this.monaco = null;
    this.isInitialized = false;
    logger.info('[MonacoFileSync] Disposed');
  }
}

// Export singleton instance
export const monacoFileSync = MonacoFileSyncService.getInstance();

/**
 * Hook to sync files when component mounts
 */
export function initializeFileSync(
  monaco: Monaco,
  config?: FileSyncConfig
): void {
  monacoFileSync.initialize(monaco, config);
}
